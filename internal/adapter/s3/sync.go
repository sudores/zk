package s3

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/zk-org/zk/internal/core"
	"golang.org/x/sync/errgroup"
)

// transferConcurrency is how many objects are moved at once. A
// synchronization waits on round trips far more than on anything else, so a
// handful in flight is the difference between seconds and minutes on a large
// notebook.
const transferConcurrency = 8

// alwaysExcluded is kept out of the synchronization whatever the configuration
// says.
//
// The search index is derived from the notes and rewritten on every indexing,
// so synchronizing it would mean a conflict every time — the same reason the
// Git backend keeps it out of its commits. The state file describes what this
// machine last saw and would be wrong anywhere else.
var alwaysExcluded = []string{
	".zk/notebook.db",
	".zk/notebook.db-wal",
	".zk/notebook.db-shm",
	StateFile,
	StateFile + ".tmp",
}

// Backend synchronizes a notebook with an S3 bucket. It implements
// core.SyncBackend.
type Backend struct {
	notebookPath string
	store        ObjectStore
	bucket       string
	prefix       string
	exclude      []string
	maxDeletes   int

	stdout io.Writer
	stderr io.Writer
	quiet  bool

	// resolvedRoot is the notebook's directory with any symbolic links
	// resolved. It is what a path is checked against, so that a notebook which
	// is itself reached through a link still works while a link inside it
	// cannot be written through.
	resolvedRoot string

	// output serializes the running commentary, which several transfers write
	// to at once.
	output sync.Mutex
}

// BackendOpts gathers what a Backend needs to run.
type BackendOpts struct {
	// NotebookPath is the notebook's own directory.
	NotebookPath string
	// Store is the bucket, or a stand-in for one.
	Store ObjectStore
	// Bucket and Prefix say where in the store the notebook lives. They are
	// recorded in the state, as a state built against one destination says
	// nothing about another.
	Bucket string
	Prefix string
	// Exclude lists globs to keep out, over and above alwaysExcluded.
	Exclude []string
	// MaxDeletes is the number of deletions beyond which the synchronization
	// stops and asks for --force.
	MaxDeletes int

	Stdout io.Writer
	Stderr io.Writer
}

// NewBackend builds a backend over the given store.
func NewBackend(opts BackendOpts) *Backend {
	return &Backend{
		notebookPath: opts.NotebookPath,
		store:        opts.Store,
		bucket:       opts.Bucket,
		prefix:       normalizePrefix(opts.Prefix),
		exclude:      opts.Exclude,
		maxDeletes:   opts.MaxDeletes,
		stdout:       opts.Stdout,
		stderr:       opts.Stderr,
	}
}

// Sync implements core.SyncBackend.
func (b *Backend) Sync(opts core.SyncOpts) error {
	b.quiet = opts.Quiet

	b.resolvedRoot = filepath.Clean(b.notebookPath)
	if resolved, err := filepath.EvalSymlinks(b.notebookPath); err == nil {
		b.resolvedRoot = resolved
	}

	// An interrupted synchronization is a normal thing to want: the state is
	// still written for whatever did go through, and the rest is planned again
	// next time.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	state, firstSync := b.loadState()

	local, err := b.scanLocal(state)
	if err != nil {
		return err
	}

	objects, err := b.store.List(ctx, b.prefix)
	if err != nil {
		return err
	}
	remote := b.remoteEntries(objects)

	b.logf("Comparing %d local files with %s...\n", len(local), b.location())

	if err := b.resolveContentHashes(ctx, local, remote, state, firstSync); err != nil {
		return err
	}

	plan, err := core.PlanSync(local, remote, state, core.SyncPlanOpts{
		FirstSync:  firstSync,
		NoPull:     opts.NoPull,
		NoPush:     opts.NoPush,
		MaxDeletes: b.maxDeletes,
		Force:      opts.Force,
	})
	if err != nil {
		return err
	}

	if plan.IsEmpty() {
		b.logf("Nothing to do: the notebook and %s already match.\n", b.location())
		return nil
	}

	if opts.DryRun {
		b.printPlan(plan)
		return nil
	}

	return b.apply(ctx, plan, local, remote, state, firstSync)
}

// loadState reads the record of the last synchronization, and says whether it
// is usable. Anything which makes it unusable — absent, corrupt, written by
// another version, or built against a different bucket — falls back to a first
// synchronization, which merges the two sides and deletes nothing.
func (b *Backend) loadState() (core.SyncState, bool) {
	state, err := LoadState(b.statePath())

	switch {
	case errors.Is(err, os.ErrNotExist):
		b.logf("No record of an earlier synchronization with %s: merging both sides, and deleting nothing.\n", b.location())
	case err != nil:
		b.logf("Ignoring the record of the last synchronization: %v\n", err)
		b.logf("Merging both sides, and deleting nothing.\n")
	case state.Bucket != b.bucket || state.Prefix != b.prefix:
		b.logf("The notebook was last synchronized with %s and is now set to use %s: merging both sides, and deleting nothing.\n",
			location(state.Bucket, state.Prefix), b.location())
	default:
		return state, false
	}

	return core.SyncState{Files: map[string]core.SyncRecord{}}, true
}

// scanLocal describes every file in the notebook which is in scope.
func (b *Backend) scanLocal(state core.SyncState) (map[string]core.SyncEntry, error) {
	entries := map[string]core.SyncEntry{}

	err := filepath.WalkDir(b.notebookPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relative, err := filepath.Rel(b.notebookPath, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "." {
			return nil
		}

		if entry.IsDir() {
			if b.isExcluded(relative) || b.isExcluded(relative+"/") {
				return fs.SkipDir
			}
			return nil
		}
		// A symbolic link, a socket or a device is not a note, and following
		// one could take the synchronization out of the notebook entirely.
		if !entry.Type().IsRegular() {
			return nil
		}
		if b.isExcluded(relative) {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}

		hash, err := b.localHash(path, relative, info, state)
		if err != nil {
			return err
		}

		entries[relative] = core.SyncEntry{
			Path:        relative,
			Size:        info.Size(),
			Hash:        hash,
			ContentHash: hash,
			Modified:    info.ModTime(),
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read the notebook: %w", err)
	}

	return entries, nil
}

// localHash returns a file's content hash, taking the last synchronization's
// word for it when the file plainly has not been touched since.
//
// Re-reading every note on every synchronization is what would make a large
// notebook slow, and a file whose size and modification time are both
// unchanged is one the state already describes. The last condition is the
// subtle one: the file's modification time must be strictly older than the
// state itself, because a file written within the same clock tick as the state
// — or afterwards, at the same size — would otherwise look unchanged while
// holding something else, and the edit would never be noticed. Git guards its
// index against the same race.
func (b *Backend) localHash(path, relative string, info fs.FileInfo, state core.SyncState) (string, error) {
	record, known := state.Files[relative]
	unchanged := known &&
		record.LocalHash != "" &&
		record.LocalSize == info.Size() &&
		record.LocalMTime.Equal(info.ModTime()) &&
		!state.SyncedAt.IsZero() &&
		info.ModTime().Before(state.SyncedAt)
	if unchanged {
		return record.LocalHash, nil
	}
	return fileMD5(path)
}

// remoteEntries describes the objects which are in scope, keyed by their path
// within the notebook rather than by their full key.
func (b *Backend) remoteEntries(objects []Object) map[string]core.SyncEntry {
	entries := make(map[string]core.SyncEntry, len(objects))

	for _, object := range objects {
		if !strings.HasPrefix(object.Key, b.prefix) {
			continue
		}
		path := strings.TrimPrefix(object.Key, b.prefix)
		// A key ending in a slash is one of the empty objects some tools write
		// to stand in for a folder, and holds nothing to synchronize.
		if path == "" || strings.HasSuffix(path, "/") {
			continue
		}
		// The bucket is not necessarily trustworthy: anything which could be
		// written outside the notebook is left where it is.
		if !isSafeRemotePath(path) {
			continue
		}
		if b.isExcluded(path) {
			continue
		}

		entry := core.SyncEntry{
			Path:     path,
			Size:     object.Size,
			Hash:     object.ETag,
			Modified: object.LastModified,
		}
		// An ETag which is a plain MD5 is the content's hash and needs no
		// further request; anything else is resolved later, and only where it
		// makes a difference.
		if IsMD5(object.ETag) {
			entry.ContentHash = object.ETag
		}
		entries[path] = entry
	}

	return entries
}

// resolveContentHashes fills in the remote content hashes the plan actually
// depends on, by reading the metadata zk wrote at upload time.
//
// Only the paths which changed on both sides need it, which is what keeps this
// to a request or two rather than one per note on a bucket whose ETags say
// nothing — every bucket encrypted with SSE-KMS, for instance.
func (b *Backend) resolveContentHashes(ctx context.Context, local, remote map[string]core.SyncEntry, state core.SyncState, firstSync bool) error {
	var mutex sync.Mutex
	resolved := map[string]string{}

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(transferConcurrency)

	for _, path := range core.SyncContentComparisonPaths(local, remote, state, firstSync) {
		if remote[path].ContentHash != "" {
			continue
		}

		group.Go(func() error {
			metadata, err := b.store.Head(groupCtx, b.key(path))
			if err != nil {
				return err
			}
			hash := metadata[MetaMD5]
			if hash == "" {
				// Nothing says what this object holds, so the plan treats it
				// as a conflict rather than guessing.
				return nil
			}

			mutex.Lock()
			defer mutex.Unlock()
			resolved[path] = hash
			return nil
		})
	}

	if err := group.Wait(); err != nil {
		return err
	}

	// Written back here rather than from the goroutines, so that `remote` is
	// touched by one goroutine at a time: the loop above still reads it while
	// the requests it has started are in flight.
	for path, hash := range resolved {
		entry := remote[path]
		entry.ContentHash = hash
		remote[path] = entry
	}

	return nil
}

// applied is what one change left behind.
//
// A change which failed leaves nothing, so the paths it touched keep whatever
// the last synchronization recorded and are planned again next time.
type applied struct {
	done bool
	// records are the paths now synchronized on both sides.
	records map[string]core.SyncRecord
	// forget are the paths to drop from the state, being gone from both sides.
	forget []string
}

// apply carries out the plan and records what went through.
//
// The state is built from the changes which succeeded, never from the plan: an
// upload which was skipped or which failed must not be recorded as done, or
// the file would look unchanged for ever and never reach the bucket.
func (b *Backend) apply(ctx context.Context, plan core.SyncPlan, local, remote map[string]core.SyncEntry, state core.SyncState, firstSync bool) error {
	// What this synchronization does not touch keeps its record. With no
	// usable state there is nothing to keep.
	files := map[string]core.SyncRecord{}
	if !firstSync {
		maps.Copy(files, state.Files)
	}

	// Remote deletions go out in one request rather than one apiece, so they
	// are held back and done together at the end.
	var remoteDeletes []string
	transfers := make([]core.SyncChange, 0, len(plan.Changes))
	for _, change := range plan.Changes {
		if change.Action == core.SyncDeleteRemote {
			remoteDeletes = append(remoteDeletes, change.Path)
			continue
		}
		transfers = append(transfers, change)
	}

	results := make([]applied, len(transfers))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(transferConcurrency)
	for i, change := range transfers {
		group.Go(func() error {
			result, err := b.applyChange(groupCtx, change, local, remote)
			if err != nil {
				return err
			}
			// Each goroutine owns its own slot, so the results need no lock.
			results[i] = result
			b.report(change)
			return nil
		})
	}
	transferErr := group.Wait()

	for _, result := range results {
		if !result.done {
			continue
		}
		maps.Copy(files, result.records)
		for _, path := range result.forget {
			delete(files, path)
		}
	}

	// Deletions wait until everything else has gone through: a half-applied
	// plan is a poor moment to start removing things.
	var deleteErr error
	if transferErr == nil && len(remoteDeletes) > 0 {
		keys := make([]string, 0, len(remoteDeletes))
		for _, path := range remoteDeletes {
			keys = append(keys, b.key(path))
		}
		if deleteErr = b.store.Delete(ctx, keys); deleteErr == nil {
			for _, path := range remoteDeletes {
				b.report(core.SyncChange{Path: path, Action: core.SyncDeleteRemote, Reason: "deleted here"})
				delete(files, path)
			}
		}
	}

	// Saved whichever way it went: what did go through must not be done twice,
	// and what did not is planned again next time.
	saveErr := SaveState(b.statePath(), core.SyncState{
		Bucket: b.bucket,
		Prefix: b.prefix,
		Files:  files,
	})

	switch {
	case transferErr != nil:
		return transferErr
	case deleteErr != nil:
		return deleteErr
	case saveErr != nil:
		return saveErr
	}

	b.summarize(plan)
	return nil
}

// applyChange carries out one change.
func (b *Backend) applyChange(ctx context.Context, change core.SyncChange, local, remote map[string]core.SyncEntry) (applied, error) {
	result := applied{records: map[string]core.SyncRecord{}}

	switch change.Action {
	case core.SyncUpload:
		record, err := b.upload(ctx, change.Path)
		if err != nil {
			return result, err
		}
		result.records[change.Path] = record

	case core.SyncDownload:
		record, err := b.download(ctx, change.Path, change.Path, remote[change.Path])
		if err != nil {
			return result, err
		}
		result.records[change.Path] = record

	case core.SyncConflict:
		records, err := b.resolveConflict(ctx, change, remote)
		if err != nil {
			return result, err
		}
		result.records = records

	case core.SyncDeleteLocal:
		if err := b.deleteLocal(change.Path); err != nil {
			return result, err
		}
		result.forget = []string{change.Path}

	case core.SyncRecordOnly:
		// Both sides already hold the same content; only the state was behind.
		localEntry, remoteEntry := local[change.Path], remote[change.Path]
		result.records[change.Path] = core.SyncRecord{
			LocalHash:  localEntry.Hash,
			LocalSize:  localEntry.Size,
			LocalMTime: localEntry.Modified,
			RemoteETag: remoteEntry.Hash,
			RemoteSize: remoteEntry.Size,
		}

	case core.SyncForget:
		result.forget = []string{change.Path}

	default:
		return result, fmt.Errorf("unexpected synchronization action for %s", change.Path)
	}

	result.done = true
	return result, nil
}

// resolveConflict keeps both versions of a note which was changed in two
// places, and is three steps rather than one:
//
//  1. their version is written beside ours, under the conflict name, so that
//     it is not lost;
//  2. our version is uploaded, so that it wins on the remote;
//  3. the conflict copy is uploaded too.
//
// Recording both paths afterwards is what makes the other machine converge on
// the same pair: it sees the note changed remotely and unchanged locally,
// takes both files, and the conflict is over. Leaving out either the upload or
// the record is a bug with a shape of its own — without the upload our version
// never reaches the bucket and the conflict is stuck for ever, and without the
// record every later synchronization finds it again and makes another copy.
func (b *Backend) resolveConflict(ctx context.Context, change core.SyncChange, remote map[string]core.SyncEntry) (map[string]core.SyncRecord, error) {
	records := map[string]core.SyncRecord{}

	// 1. Their version, kept under the conflict name.
	if _, err := b.download(ctx, change.Path, change.ConflictPath, remote[change.Path]); err != nil {
		return nil, err
	}

	// 2. Ours, which the remote now takes.
	record, err := b.upload(ctx, change.Path)
	if err != nil {
		return nil, err
	}
	records[change.Path] = record

	// 3. Theirs, under its new name, so the other machine gets it too. The
	// record comes from the upload rather than from the download, as only the
	// upload knows the new object's ETag.
	conflictRecord, err := b.upload(ctx, change.ConflictPath)
	if err != nil {
		return nil, err
	}
	records[change.ConflictPath] = conflictRecord

	return records, nil
}

// upload sends a local file out and returns what to record for it.
//
// The file is measured and hashed from the handle which is about to be read,
// so that the record and the metadata cannot end up describing a version other
// than the one uploaded.
func (b *Backend) upload(ctx context.Context, path string) (core.SyncRecord, error) {
	full := b.localPath(path)

	file, err := os.Open(full)
	if err != nil {
		return core.SyncRecord{}, fmt.Errorf("failed to read %s: %w", path, err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return core.SyncRecord{}, fmt.Errorf("failed to read %s: %w", path, err)
	}

	hash := md5.New()
	if _, err := io.Copy(hash, file); err != nil {
		return core.SyncRecord{}, fmt.Errorf("failed to read %s: %w", path, err)
	}
	contentHash := hex.EncodeToString(hash.Sum(nil))

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return core.SyncRecord{}, fmt.Errorf("failed to read %s: %w", path, err)
	}

	etag, err := b.store.Put(ctx, b.key(path), file, info.Size(), map[string]string{
		MetaMD5:   contentHash,
		MetaMTime: info.ModTime().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return core.SyncRecord{}, err
	}

	return core.SyncRecord{
		LocalHash:  contentHash,
		LocalSize:  info.Size(),
		LocalMTime: info.ModTime(),
		RemoteETag: etag,
		RemoteSize: info.Size(),
	}, nil
}

// download brings an object in and writes it to destination, which is the
// object's own path except when a conflict sends it somewhere else.
func (b *Backend) download(ctx context.Context, path, destination string, entry core.SyncEntry) (core.SyncRecord, error) {
	reader, err := b.store.Get(ctx, b.key(path))
	if err != nil {
		return core.SyncRecord{}, err
	}
	defer reader.Body.Close()

	full := b.localPath(destination)

	// A symbolic link is never one of the local entries, as the scan only
	// looks at regular files. Without this the planner would take the
	// bucket's copy for a file which exists only on the remote, and the
	// download would replace the link - or, for a linked directory, write
	// straight through it and out of the notebook.
	if info, err := os.Lstat(full); err == nil && !info.Mode().IsRegular() {
		return core.SyncRecord{}, fmt.Errorf("refusing to replace %s: it is not a regular file", destination)
	}

	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return core.SyncRecord{}, fmt.Errorf("failed to write %s: %w", destination, err)
	}
	if err := b.checkWithinNotebook(filepath.Dir(full), destination); err != nil {
		return core.SyncRecord{}, err
	}

	// Written beside the note and moved into place, so that an interrupted
	// download cannot leave half a note behind.
	temp, err := os.CreateTemp(filepath.Dir(full), ".zk-sync-*")
	if err != nil {
		return core.SyncRecord{}, fmt.Errorf("failed to write %s: %w", destination, err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)

	hash := md5.New()
	size, err := io.Copy(io.MultiWriter(temp, hash), reader.Body)
	if err != nil {
		temp.Close()
		return core.SyncRecord{}, fmt.Errorf("failed to download %s: %w", path, err)
	}
	if err := temp.Close(); err != nil {
		return core.SyncRecord{}, fmt.Errorf("failed to write %s: %w", destination, err)
	}
	if err := os.Chmod(tempName, 0o644); err != nil {
		return core.SyncRecord{}, fmt.Errorf("failed to write %s: %w", destination, err)
	}
	if err := os.Rename(tempName, full); err != nil {
		return core.SyncRecord{}, fmt.Errorf("failed to write %s: %w", destination, err)
	}

	// zk shows a note's modification time as its modification date and sorts
	// by it, so a downloaded note keeps the time it was written rather than
	// the time it arrived here.
	modified := entry.Modified
	if stamp, ok := reader.Metadata[MetaMTime]; ok {
		if parsed, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
			modified = parsed
		}
	}
	if !modified.IsZero() {
		if err := os.Chtimes(full, modified, modified); err != nil {
			return core.SyncRecord{}, fmt.Errorf("failed to set the modification time of %s: %w", destination, err)
		}
	}

	// Read back rather than assumed: a filesystem which keeps a coarser time
	// than we asked for would otherwise make the next scan re-read the file.
	if info, err := os.Stat(full); err == nil {
		modified = info.ModTime()
	}

	return core.SyncRecord{
		LocalHash:  hex.EncodeToString(hash.Sum(nil)),
		LocalSize:  size,
		LocalMTime: modified,
		RemoteETag: entry.Hash,
		RemoteSize: entry.Size,
	}, nil
}

// checkWithinNotebook refuses a directory which, once its symbolic links are
// resolved, is not inside the notebook.
//
// It is the companion to isSafeRemotePath: that one catches a key which
// escapes by naming `..`, and this one catches a key which escapes through a
// link that happens to be sitting in the notebook.
func (b *Backend) checkWithinNotebook(dir, destination string) error {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("failed to write %s: %w", destination, err)
	}
	if resolved == b.resolvedRoot || strings.HasPrefix(resolved, b.resolvedRoot+string(filepath.Separator)) {
		return nil
	}
	return fmt.Errorf("refusing to write %s: %s leads outside the notebook", destination, dir)
}

// deleteLocal removes a note which was deleted elsewhere.
func (b *Backend) deleteLocal(path string) error {
	full := b.localPath(path)
	if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to delete %s: %w", path, err)
	}
	b.pruneEmptyDirs(filepath.Dir(full))
	return nil
}

// pruneEmptyDirs removes the directories a deletion has emptied, stopping at
// the notebook itself. An empty directory is not a note, and S3 has no
// directories to have emptied in the first place.
func (b *Backend) pruneEmptyDirs(dir string) {
	root := filepath.Clean(b.notebookPath)
	zkDir := filepath.Join(root, ".zk")
	for {
		dir = filepath.Clean(dir)
		if dir == root || !strings.HasPrefix(dir, root+string(filepath.Separator)) {
			return
		}
		// `.zk` is what makes the directory a notebook, so it stays even when
		// there is nothing left in it.
		if dir == zkDir {
			return
		}
		// Removing a symbolic link succeeds however much its target holds, so
		// only a directory which really is one is pruned.
		if info, err := os.Lstat(dir); err != nil || !info.Mode().IsDir() {
			return
		}
		// Removal fails on a directory which still holds something, which is
		// exactly when it should be left alone.
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// isSafeRemotePath reports whether a key from the bucket may be written to
// disk at all.
//
// The paths come from the remote rather than from the notebook, so they are
// checked rather than trusted: a key of `../../.bashrc` would otherwise be
// written outside the notebook entirely. A backslash is refused too, as it is
// a path separator on Windows and would slip past a check on slashes alone.
func isSafeRemotePath(path string) bool {
	if path == "" || strings.HasPrefix(path, "/") || strings.Contains(path, "\\") {
		return false
	}
	// filepath.IsAbs would not catch a Windows drive letter on another
	// platform, so the check is made on the shape of the path itself.
	if len(path) > 1 && path[1] == ':' {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == ".." {
			return false
		}
	}
	return true
}

// isExcluded reports whether a path is out of scope. It is asked of both sides,
// so that an excluded file is neither uploaded nor downloaded.
func (b *Backend) isExcluded(path string) bool {
	for _, excluded := range alwaysExcluded {
		if path == excluded {
			return true
		}
	}
	// A Git repository's internals are not notes, and a notebook may well be
	// one — the two backends are not mutually exclusive.
	if path == ".git" || strings.HasPrefix(path, ".git/") {
		return true
	}

	for _, pattern := range b.exclude {
		// A malformed glob excludes nothing rather than stopping the
		// synchronization, in keeping with how `note.exclude` behaves.
		if matched, err := doublestar.Match(pattern, path); err == nil && matched {
			return true
		}
	}
	return false
}

// printPlan reports what a dry run would have done. It goes to standard output
// because it is the result the command was asked for, rather than commentary.
func (b *Backend) printPlan(plan core.SyncPlan) {
	for _, change := range plan.Changes {
		fmt.Fprintf(b.stdout, "%s\n", describe(change))
	}
}

// report says what has just been done, as it is done.
func (b *Backend) report(change core.SyncChange) {
	if b.quiet {
		return
	}
	b.output.Lock()
	defer b.output.Unlock()
	fmt.Fprintf(b.stderr, "  %s\n", describe(change))
}

// describe renders one change, in a form which is the same for a dry run and
// for the real thing.
func describe(change core.SyncChange) string {
	reason := change.Reason
	if change.Action == core.SyncConflict {
		reason = fmt.Sprintf("%s; their version kept in %s", change.Reason, change.ConflictPath)
	}
	return fmt.Sprintf("%-13s %s (%s)", change.Action, change.Path, reason)
}

// summarize closes with a count, so that an automated synchronization leaves
// one line in a log rather than a file list.
func (b *Backend) summarize(plan core.SyncPlan) {
	conflicts := plan.Count(core.SyncConflict)

	// Resolving one conflict downloads their version and uploads two files:
	// ours and the copy of theirs. Counting only the plain actions would
	// report "0 uploaded" for a run which did send notes out.
	downloaded := plan.Count(core.SyncDownload) + conflicts
	uploaded := plan.Count(core.SyncUpload) + 2*conflicts

	var parts []string
	for _, part := range []struct {
		count int
		label string
	}{
		{downloaded, "downloaded"},
		{uploaded, "uploaded"},
		{plan.Count(core.SyncDeleteLocal), "deleted here"},
		{plan.Count(core.SyncDeleteRemote), "deleted there"},
	} {
		if part.count > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", part.count, part.label))
		}
	}
	if conflicts > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", conflicts, plural(conflicts, "conflict", "conflicts")))
	}
	// Everything in the plan was a correction to the record rather than a
	// transfer, which is worth saying plainly rather than as four zeroes.
	if len(parts) == 0 {
		parts = append(parts, "nothing moved")
	}

	b.logf("Synchronized with %s: %s.\n", b.location(), strings.Join(parts, ", "))

	if conflicts > 0 {
		b.logf("%s changed in two places. Both versions were kept: yours under its own name, and theirs beside it.\n",
			plural(conflicts, "A note was", fmt.Sprintf("%d notes were", conflicts)))
	}
}

// plural picks the wording for a count, so that a summary does not report
// "1 conflicts".
func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}

func (b *Backend) logf(format string, args ...any) {
	if b.quiet {
		return
	}
	b.output.Lock()
	defer b.output.Unlock()
	fmt.Fprintf(b.stderr, format, args...)
}

// key is the object key a notebook path lives under.
func (b *Backend) key(path string) string {
	return b.prefix + path
}

// localPath is the file a notebook path refers to.
func (b *Backend) localPath(path string) string {
	return filepath.Join(b.notebookPath, filepath.FromSlash(path))
}

func (b *Backend) statePath() string {
	return filepath.Join(b.notebookPath, filepath.FromSlash(StateFile))
}

func (b *Backend) location() string {
	return location(b.bucket, b.prefix)
}

func location(bucket, prefix string) string {
	return "s3://" + bucket + "/" + prefix
}

// normalizePrefix makes a prefix either empty or a clean, slash-terminated
// path, so that keys are built by concatenation alone.
func normalizePrefix(prefix string) string {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return ""
	}
	return prefix + "/"
}

// fileMD5 hashes a file's content.
func fileMD5(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("failed to read %s: %w", path, err)
	}
	defer file.Close()

	hash := md5.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("failed to read %s: %w", path, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
