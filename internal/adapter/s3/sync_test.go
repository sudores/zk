package s3

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/zk-org/zk/internal/core"
	"github.com/zk-org/zk/internal/util/test/assert"
)

const (
	testBucket = "notes"
	testPrefix = "notebook"
)

// machine stands for one computer with its own copy of the notebook. Several of
// them synchronize through the same store, which is the only way to catch the
// bugs that matter here: whether two machines converge.
type machine struct {
	t     *testing.T
	name  string
	dir   string
	store *memStore
}

func newMachine(t *testing.T, base, name string, store *memStore) *machine {
	t.Helper()

	dir := filepath.Join(base, name)
	if err := os.MkdirAll(filepath.Join(dir, ".zk"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &machine{t: t, name: name, dir: dir, store: store}
}

func (m *machine) backend() *Backend {
	return NewBackend(BackendOpts{
		NotebookPath: m.dir,
		Store:        m.store,
		Bucket:       testBucket,
		Prefix:       testPrefix,
		MaxDeletes:   10,
		Stdout:       io.Discard,
		Stderr:       io.Discard,
	})
}

func (m *machine) sync(opts ...core.SyncOpts) {
	m.t.Helper()

	options := core.SyncOpts{Quiet: true}
	if len(opts) > 0 {
		options = opts[0]
		options.Quiet = true
	}
	if err := m.backend().Sync(options); err != nil {
		m.t.Fatalf("%s: synchronization failed: %v", m.name, err)
	}
}

func (m *machine) syncExpectingError(opts core.SyncOpts) error {
	m.t.Helper()

	opts.Quiet = true
	err := m.backend().Sync(opts)
	if err == nil {
		m.t.Fatalf("%s: expected the synchronization to fail", m.name)
	}
	return err
}

func (m *machine) write(path, content string) {
	m.t.Helper()

	full := filepath.Join(m.dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		m.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		m.t.Fatal(err)
	}
}

func (m *machine) read(path string) string {
	m.t.Helper()

	content, err := os.ReadFile(filepath.Join(m.dir, path))
	if err != nil {
		m.t.Fatalf("%s: %v", m.name, err)
	}
	return string(content)
}

func (m *machine) remove(path string) {
	m.t.Helper()

	if err := os.Remove(filepath.Join(m.dir, path)); err != nil {
		m.t.Fatal(err)
	}
}

func (m *machine) exists(path string) bool {
	_, err := os.Stat(filepath.Join(m.dir, path))
	return err == nil
}

// notes lists the Markdown files the notebook holds, sorted.
func (m *machine) notes() []string {
	m.t.Helper()

	var notes []string
	err := filepath.WalkDir(m.dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		relative, err := filepath.Rel(m.dir, path)
		if err != nil {
			return err
		}
		notes = append(notes, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		m.t.Fatal(err)
	}
	sort.Strings(notes)
	return notes
}

func twoMachines(t *testing.T) (*memStore, *machine, *machine) {
	t.Helper()

	base := t.TempDir()
	store := newMemStore()
	return store, newMachine(t, base, "a", store), newMachine(t, base, "b", store)
}

// TestSyncRoundTrip is the plain case: a note written here turns up there.
func TestSyncRoundTrip(t *testing.T) {
	store, a, b := twoMachines(t)

	a.write("hello.md", "# Hello")
	a.sync()

	assert.Equal(t, store.keys(), []string{"notebook/hello.md"})

	b.sync()
	assert.Equal(t, b.notes(), []string{"hello.md"})
	assert.Equal(t, b.read("hello.md"), "# Hello")

	// A note in a subdirectory keeps its place.
	b.write("journal/monday.md", "Monday")
	b.sync()
	a.sync()
	assert.Equal(t, a.read("journal/monday.md"), "Monday")
}

// TestSyncIsIdempotent checks that a synchronization with nothing to do moves
// nothing, which is what keeps an automated one cheap.
func TestSyncIsIdempotent(t *testing.T) {
	store, a, b := twoMachines(t)

	a.write("hello.md", "# Hello")
	a.sync()
	b.sync()

	before := store.puts
	a.sync()
	b.sync()
	assert.Equal(t, store.puts, before)
}

// TestSyncEditPropagates checks that a change made here replaces the older
// version there.
func TestSyncEditPropagates(t *testing.T) {
	_, a, b := twoMachines(t)

	a.write("hello.md", "first")
	a.sync()
	b.sync()

	a.write("hello.md", "second")
	a.sync()
	b.sync()

	assert.Equal(t, b.read("hello.md"), "second")
}

// TestSyncDeletionPropagates checks that a note deleted here goes from there
// too, which is the whole reason for keeping a state file.
func TestSyncDeletionPropagates(t *testing.T) {
	store, a, b := twoMachines(t)

	a.write("hello.md", "# Hello")
	a.write("keep.md", "# Keep")
	a.sync()
	b.sync()

	a.remove("hello.md")
	a.sync()
	assert.Equal(t, store.keys(), []string{"notebook/keep.md"})

	b.sync()
	assert.Equal(t, b.notes(), []string{"keep.md"})

	// And it stays gone, rather than coming back on the next run.
	a.sync()
	b.sync()
	assert.Equal(t, a.notes(), []string{"keep.md"})
	assert.Equal(t, b.notes(), []string{"keep.md"})
}

// TestSyncConflictConverges is the test the whole design exists for.
//
// The same note is edited in two places. Both versions must survive, both
// machines must end up with the same pair, and — the part easiest to get wrong
// — a further synchronization must then do nothing at all, rather than make
// another copy or leave the conflict unresolved for ever.
func TestSyncConflictConverges(t *testing.T) {
	store, a, b := twoMachines(t)

	a.write("meeting.md", "shared")
	a.sync()
	b.sync()

	a.write("meeting.md", "written on a")
	b.write("meeting.md", "written on b")

	// A goes first, so its version is simply uploaded.
	a.sync()
	assert.Equal(t, store.content("notebook/meeting.md"), "written on a")

	// B now finds the note changed on both sides. It keeps its own under the
	// original name and takes A's beside it.
	b.sync()
	assert.Equal(t, b.notes(), []string{"meeting.conflict.md", "meeting.md"})
	assert.Equal(t, b.read("meeting.md"), "written on b")
	assert.Equal(t, b.read("meeting.conflict.md"), "written on a")

	// B's version has to have reached the bucket, or the conflict would be
	// stuck for ever with each machine keeping its own.
	assert.Equal(t, store.content("notebook/meeting.md"), "written on b")
	assert.Equal(t, store.content("notebook/meeting.conflict.md"), "written on a")

	// A catches up and lands on the same pair. Nothing is lost on either side.
	a.sync()
	assert.Equal(t, a.notes(), []string{"meeting.conflict.md", "meeting.md"})
	assert.Equal(t, a.read("meeting.md"), "written on b")
	assert.Equal(t, a.read("meeting.conflict.md"), "written on a")

	// The conflict is over: a third run moves nothing and makes no further
	// copy.
	before := store.puts
	a.sync()
	b.sync()
	assert.Equal(t, store.puts, before)
	assert.Equal(t, a.notes(), []string{"meeting.conflict.md", "meeting.md"})
	assert.Equal(t, b.notes(), []string{"meeting.conflict.md", "meeting.md"})
}

// TestSyncConflictWithOpaqueETags checks that a bucket whose ETags say nothing
// about the content — SSE-KMS, or a multipart upload — does not turn every
// note into a conflict.
func TestSyncConflictWithOpaqueETags(t *testing.T) {
	store, a, b := twoMachines(t)
	store.opaqueETags = true

	a.write("hello.md", "# Hello")
	a.write("other.md", "# Other")
	a.sync()
	b.sync()

	// Both machines write the same thing, which is an agreement and not a
	// conflict. Only the metadata zk stores can tell, as the ETag cannot.
	a.write("hello.md", "# Agreed")
	b.write("hello.md", "# Agreed")
	a.sync()
	b.sync()

	assert.Equal(t, b.notes(), []string{"hello.md", "other.md"})
	assert.Equal(t, b.read("hello.md"), "# Agreed")

	// A real disagreement is still caught.
	a.write("other.md", "from a")
	b.write("other.md", "from b")
	a.sync()
	b.sync()
	assert.Equal(t, b.notes(), []string{"hello.md", "other.conflict.md", "other.md"})
}

// TestSyncFirstSyncMerges checks the safety property: with no state to go by,
// the two sides are merged and nothing is deleted. It is what makes losing the
// state file harmless.
func TestSyncFirstSyncMerges(t *testing.T) {
	store, a, b := twoMachines(t)

	a.write("shared.md", "# Shared")
	a.write("only-on-a.md", "# A")
	a.sync()

	b.write("only-on-b.md", "# B")
	b.sync()

	assert.Equal(t, b.notes(), []string{"only-on-a.md", "only-on-b.md", "shared.md"})
	assert.Equal(t, store.keys(), []string{
		"notebook/only-on-a.md", "notebook/only-on-b.md", "notebook/shared.md",
	})

	// Throwing the state away must not make the next run delete anything,
	// even though every note is now unknown to it.
	if err := os.Remove(filepath.Join(b.dir, StateFile)); err != nil {
		t.Fatal(err)
	}
	b.sync()
	assert.Equal(t, b.notes(), []string{"only-on-a.md", "only-on-b.md", "shared.md"})
	assert.Equal(t, store.keys(), []string{
		"notebook/only-on-a.md", "notebook/only-on-b.md", "notebook/shared.md",
	})

	// Nor when the state is corrupt rather than missing.
	if err := os.WriteFile(filepath.Join(b.dir, StateFile), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	b.sync()
	assert.Equal(t, b.notes(), []string{"only-on-a.md", "only-on-b.md", "shared.md"})
}

// TestSyncNoPushIsResumed is the state-keeping rule in action: a skipped
// upload must not be recorded as done, or the note would look unchanged for
// ever and never reach the bucket.
func TestSyncNoPushIsResumed(t *testing.T) {
	store, a, _ := twoMachines(t)

	a.write("hello.md", "# Hello")
	a.sync(core.SyncOpts{NoPush: true})
	assert.Equal(t, store.keys(), []string{})

	a.sync()
	assert.Equal(t, store.keys(), []string{"notebook/hello.md"})
	assert.Equal(t, store.content("notebook/hello.md"), "# Hello")
}

// TestSyncNoPullIsResumed is the mirror image: a download which was skipped is
// still pending afterwards.
func TestSyncNoPullIsResumed(t *testing.T) {
	_, a, b := twoMachines(t)

	a.write("hello.md", "# Hello")
	a.sync()

	b.sync(core.SyncOpts{NoPull: true})
	assert.False(t, b.exists("hello.md"))

	b.sync()
	assert.Equal(t, b.read("hello.md"), "# Hello")
}

// TestSyncResumesAfterAFailedUpload checks that a transfer dying halfway
// leaves a state which is merely out of date: what went through is recorded,
// and the rest is picked up next time.
func TestSyncResumesAfterAFailedUpload(t *testing.T) {
	store, a, _ := twoMachines(t)

	for _, name := range []string{"a.md", "b.md", "c.md", "d.md"} {
		a.write(name, "# "+name)
	}

	// Only two of the four uploads are allowed through.
	store.failPutsAfter = 2
	err := a.syncExpectingError(core.SyncOpts{})
	assert.Err(t, err, "refusing to upload")
	assert.Equal(t, len(store.keys()), 2)

	// With the store willing again, the remaining notes go up and nothing is
	// uploaded twice.
	store.failPutsAfter = 0
	a.sync()
	assert.Equal(t, store.keys(), []string{
		"notebook/a.md", "notebook/b.md", "notebook/c.md", "notebook/d.md",
	})

	before := store.puts
	a.sync()
	assert.Equal(t, store.puts, before)
}

// TestSyncDeleteGuard checks that a state describing far more notes than are
// left cannot quietly empty a bucket.
func TestSyncDeleteGuard(t *testing.T) {
	store, a, _ := twoMachines(t)

	for _, name := range []string{"a.md", "b.md", "c.md", "d.md"} {
		a.write(name, "# "+name)
	}
	a.sync()

	for _, name := range []string{"a.md", "b.md", "c.md", "d.md"} {
		a.remove(name)
	}

	backend := NewBackend(BackendOpts{
		NotebookPath: a.dir, Store: store,
		Bucket: testBucket, Prefix: testPrefix,
		MaxDeletes: 3,
		Stdout:     io.Discard, Stderr: io.Discard,
	})
	err := backend.Sync(core.SyncOpts{Quiet: true})
	assert.Err(t, err, "it would delete 4 files")
	assert.Equal(t, len(store.keys()), 4)

	// --force is the way through, and it does go through.
	err = backend.Sync(core.SyncOpts{Quiet: true, Force: true})
	assert.Nil(t, err)
	assert.Equal(t, store.keys(), []string{})
}

// TestSyncExcludesTheIndexAndTheState checks that what must never travel does
// not: the search index is derived from the notes, and the state describes one
// machine only.
func TestSyncExcludesTheIndexAndTheState(t *testing.T) {
	store, a, b := twoMachines(t)

	a.write("hello.md", "# Hello")
	a.write(".zk/config.toml", "# the configuration does travel")
	a.write(".zk/notebook.db", "binary index")
	a.write(".zk/notebook.db-wal", "write-ahead log")
	a.sync()

	assert.Equal(t, store.keys(), []string{"notebook/.zk/config.toml", "notebook/hello.md"})

	b.sync()
	assert.True(t, b.exists(".zk/config.toml"))
	assert.False(t, b.exists(".zk/notebook.db"))
	assert.False(t, b.exists(StateFile+".tmp"))
}

// TestSyncHonoursExclude checks the configured globs.
func TestSyncHonoursExclude(t *testing.T) {
	store, a, _ := twoMachines(t)

	a.write("hello.md", "# Hello")
	a.write("attachments/big.png", "not a note")
	a.write("scratch.tmp", "temporary")

	backend := NewBackend(BackendOpts{
		NotebookPath: a.dir, Store: store,
		Bucket: testBucket, Prefix: testPrefix,
		Exclude:    []string{"attachments/**", "*.tmp"},
		MaxDeletes: 10,
		Stdout:     io.Discard, Stderr: io.Discard,
	})
	assert.Nil(t, backend.Sync(core.SyncOpts{Quiet: true}))

	assert.Equal(t, store.keys(), []string{"notebook/hello.md"})
}

// TestSyncKeepsModificationTimes checks that a downloaded note keeps the time
// it was written, rather than the time it arrived: zk shows that as the note's
// modification date and sorts by it.
func TestSyncKeepsModificationTimes(t *testing.T) {
	_, a, b := twoMachines(t)

	a.write("hello.md", "# Hello")
	original, err := os.Stat(filepath.Join(a.dir, "hello.md"))
	assert.Nil(t, err)

	a.sync()
	b.sync()

	downloaded, err := os.Stat(filepath.Join(b.dir, "hello.md"))
	assert.Nil(t, err)

	// A filesystem may keep a coarser time than we asked for, so the two are
	// compared to the second rather than exactly.
	if downloaded.ModTime().Unix() != original.ModTime().Unix() {
		t.Errorf("expected the modification time to survive the round trip: %v, got %v",
			original.ModTime(), downloaded.ModTime())
	}
}

// TestSyncDryRunChangesNothing checks that a dry run is exactly that, on both
// sides, and that it prints what it would have done.
func TestSyncDryRunChangesNothing(t *testing.T) {
	store, a, b := twoMachines(t)

	a.write("hello.md", "# Hello")
	a.sync()

	b.write("mine.md", "# Mine")

	var out strings.Builder
	backend := NewBackend(BackendOpts{
		NotebookPath: b.dir, Store: store,
		Bucket: testBucket, Prefix: testPrefix,
		MaxDeletes: 10,
		Stdout:     &out, Stderr: io.Discard,
	})
	assert.Nil(t, backend.Sync(core.SyncOpts{DryRun: true, Quiet: true}))

	printed := out.String()
	if !strings.Contains(printed, "download") || !strings.Contains(printed, "hello.md") {
		t.Errorf("expected the dry run to report the download, got:\n%s", printed)
	}
	if !strings.Contains(printed, "upload") || !strings.Contains(printed, "mine.md") {
		t.Errorf("expected the dry run to report the upload, got:\n%s", printed)
	}

	// Neither side moved, and no state was written.
	assert.Equal(t, store.keys(), []string{"notebook/hello.md"})
	assert.False(t, b.exists("hello.md"))
	assert.False(t, b.exists(StateFile))
}

// TestSyncPrefixIsolatesNotebooks checks that one bucket can hold several
// notebooks without them seeing each other.
func TestSyncPrefixIsolatesNotebooks(t *testing.T) {
	base := t.TempDir()
	store := newMemStore()

	first := newMachine(t, base, "first", store)
	first.write("hello.md", "# First")
	first.sync()

	second := newMachine(t, base, "second", store)
	backend := NewBackend(BackendOpts{
		NotebookPath: second.dir, Store: store,
		Bucket: testBucket, Prefix: "other",
		MaxDeletes: 10,
		Stdout:     io.Discard, Stderr: io.Discard,
	})
	second.write("mine.md", "# Second")
	assert.Nil(t, backend.Sync(core.SyncOpts{Quiet: true}))

	assert.Equal(t, store.keys(), []string{"notebook/hello.md", "other/mine.md"})
	assert.False(t, second.exists("hello.md"))
}

// TestSyncChangingTheBucketStartsOver checks that pointing a notebook at a
// different destination does not let the old state delete anything: the state
// describes where it was built, and nowhere else.
func TestSyncChangingTheBucketStartsOver(t *testing.T) {
	store, a, _ := twoMachines(t)

	a.write("hello.md", "# Hello")
	a.sync()

	moved := NewBackend(BackendOpts{
		NotebookPath: a.dir, Store: store,
		Bucket: testBucket, Prefix: "elsewhere",
		MaxDeletes: 10,
		Stdout:     io.Discard, Stderr: io.Discard,
	})
	assert.Nil(t, moved.Sync(core.SyncOpts{Quiet: true}))

	// Uploaded afresh under the new prefix, and the old location left alone.
	assert.Equal(t, store.keys(), []string{"elsewhere/hello.md", "notebook/hello.md"})
}

// writeFile and readFile are the plain helpers the SDK-level tests use, which
// have no machine to go through.
func writeFile(t *testing.T, dir, path, content string) {
	t.Helper()

	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, dir, path string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(dir, path))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// TestSyncRefusesUnsafeRemotePaths checks that a bucket cannot write outside
// the notebook. The keys come from the remote rather than from the notes, so
// they are checked rather than trusted.
func TestSyncRefusesUnsafeRemotePaths(t *testing.T) {
	base := t.TempDir()
	store := newMemStore()
	m := newMachine(t, base, "a", store)

	// A sentinel outside the notebook, which a traversing key would reach.
	outside := filepath.Join(base, "outside.md")
	if err := os.WriteFile(outside, []byte("untouched"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{
		"notebook/../outside.md",
		"notebook/../../outside.md",
		"notebook//etc/passwd",
		"notebook/sub/../../outside.md",
	} {
		if _, err := store.Put(t.Context(), key, strings.NewReader("evil"), 4, nil); err != nil {
			t.Fatal(err)
		}
	}
	// One legitimate note, so the synchronization has something to do.
	if _, err := store.Put(t.Context(), "notebook/fine.md", strings.NewReader("# Fine"), 6, nil); err != nil {
		t.Fatal(err)
	}

	m.sync()

	assert.Equal(t, m.notes(), []string{"fine.md"})
	content, err := os.ReadFile(outside)
	assert.Nil(t, err)
	assert.Equal(t, string(content), "untouched")
}

func TestIsSafeRemotePath(t *testing.T) {
	for _, path := range []string{"note.md", "a/b/note.md", "..hidden.md", "a..b/c.md"} {
		if !isSafeRemotePath(path) {
			t.Errorf("expected %q to be safe", path)
		}
	}
	for _, path := range []string{"", "/etc/passwd", "../note.md", "a/../../note.md", `a\b.md`, "C:/note.md"} {
		if isSafeRemotePath(path) {
			t.Errorf("expected %q to be refused", path)
		}
	}
}

// TestSyncKeepsTheZkDirectory checks that deleting the last file inside `.zk`
// does not take the directory with it: `.zk` is what makes the notebook one.
func TestSyncKeepsTheZkDirectory(t *testing.T) {
	store, a, b := twoMachines(t)

	a.write("hello.md", "# Hello")
	a.write(".zk/config.toml", "# config")
	a.sync()
	b.sync()
	assert.True(t, b.exists(".zk/config.toml"))

	a.remove(".zk/config.toml")
	a.sync()
	b.sync()

	assert.False(t, b.exists(".zk/config.toml"))
	// The directory itself is still there, holding the state file.
	info, err := os.Stat(filepath.Join(b.dir, ".zk"))
	assert.Nil(t, err)
	assert.True(t, info.IsDir())
	assert.Equal(t, store.keys(), []string{"notebook/hello.md"})
}

// TestSyncRefusesToWriteThroughSymlinks checks that a link inside the notebook
// cannot be used to write outside it, or be quietly replaced.
//
// The scan skips anything which is not a regular file, so a symbolic link is
// never one of the local entries. Without a guard the planner would then see
// the bucket's copy as a file which exists only on the remote, and the
// download would write straight through the link.
func TestSyncRefusesToWriteThroughSymlinks(t *testing.T) {
	base := t.TempDir()
	store := newMemStore()
	m := newMachine(t, base, "a", store)

	// Somewhere outside the notebook, reached by a link inside it.
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("untouched"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A linked directory, and a linked file.
	if err := os.Symlink(outside, filepath.Join(m.dir, "linked")); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(m.dir, "note.md")); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{"notebook/linked/secret.md", "notebook/note.md"} {
		if _, err := store.Put(t.Context(), key, strings.NewReader("evil"), 4, nil); err != nil {
			t.Fatal(err)
		}
	}

	// The synchronization fails rather than writing through either link.
	err := m.backend().Sync(core.SyncOpts{Quiet: true})
	assert.NotNil(t, err)

	content, readErr := os.ReadFile(filepath.Join(outside, "secret.md"))
	assert.Nil(t, readErr)
	assert.Equal(t, string(content), "untouched")

	// And the links themselves are still links, not replaced by files.
	for _, name := range []string{"linked", "note.md"} {
		info, statErr := os.Lstat(filepath.Join(m.dir, name))
		assert.Nil(t, statErr)
		if info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("expected %s to still be a symbolic link", name)
		}
	}
}
