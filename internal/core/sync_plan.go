package core

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// SyncEntry describes one file as it is now, on one side of a
// synchronization.
type SyncEntry struct {
	// Path is slash-separated and relative to the notebook's directory.
	Path string
	// Size is the size of the content in bytes.
	Size int64
	// Hash is the token used to notice that this side changed since the last
	// synchronization. It is the content's MD5 locally and the object's ETag
	// remotely, and the two are never compared with each other: each side is
	// only ever compared against what it was at the last synchronization.
	Hash string
	// ContentHash is the MD5 of the content, when it is known. It is what
	// answers "did both sides change to the same thing?", which an ETag cannot
	// always answer: it is the content's MD5 for an object uploaded in one
	// part and stored unencrypted or with SSE-S3, and opaque under SSE-KMS,
	// SSE-C or a multipart upload. It is empty when the content is unknown,
	// and the comparison then reports a conflict rather than guessing.
	ContentHash string
	// Modified is the file's modification time.
	Modified time.Time
}

// SyncRecord is what one file looked like on both sides at the end of the last
// synchronization.
//
// There is one record per path, holding both sides, rather than a local and a
// remote map: a path is either known to the last synchronization or it is not,
// and two maps would allow the two sides to disagree about which.
type SyncRecord struct {
	LocalHash  string
	LocalSize  int64
	LocalMTime time.Time
	RemoteETag string
	RemoteSize int64
}

// SyncState is what the last successful synchronization left behind. It is the
// base of the three-way comparison: without it, a file which appeared on one
// side and one which disappeared from the other cannot be told apart.
type SyncState struct {
	// Bucket and Prefix record what the state was built against. Pointing the
	// notebook at a different destination invalidates it, as the paths it
	// holds no longer describe what is there.
	Bucket string
	Prefix string
	// SyncedAt is when the state was written.
	//
	// It is what makes it safe to trust a file's size and modification time
	// instead of reading it again: a file whose modification time is not
	// strictly older than this could have been written within the same clock
	// tick as the state itself, and would then look unchanged while holding
	// something else. Git guards its own index the same way.
	SyncedAt time.Time
	// Files is keyed by the same slash-separated relative path as SyncEntry.
	Files map[string]SyncRecord
}

// SyncAction is what a plan does about one path.
type SyncAction int

const (
	// SyncUpload sends the local file out.
	SyncUpload SyncAction = iota
	// SyncDownload brings the remote file in, replacing the local one.
	SyncDownload
	// SyncConflict resolves a file which changed on both sides. Applying it is
	// three steps and not one: the remote version is written to ConflictPath,
	// the local file is uploaded so that the local version wins on the remote,
	// and the conflict copy is uploaded too. Both paths are then recorded as
	// synchronized, which is what makes the other machine converge on the same
	// pair rather than detecting the conflict all over again.
	SyncConflict
	// SyncDeleteLocal removes a local file which was deleted on the remote.
	SyncDeleteLocal
	// SyncDeleteRemote removes a remote object whose file was deleted locally.
	SyncDeleteRemote
	// SyncRecordOnly moves nothing: both sides changed to the same content, so
	// only the state is behind.
	SyncRecordOnly
	// SyncForget drops a path from the state, it having gone from both sides.
	SyncForget
)

// String returns the action's name, as the running commentary prints it.
func (a SyncAction) String() string {
	switch a {
	case SyncUpload:
		return "upload"
	case SyncDownload:
		return "download"
	case SyncConflict:
		return "conflict"
	case SyncDeleteLocal:
		return "delete local"
	case SyncDeleteRemote:
		return "delete remote"
	case SyncRecordOnly:
		return "up to date"
	case SyncForget:
		return "forget"
	default:
		return "unknown"
	}
}

// SyncChange is one path's worth of a plan.
type SyncChange struct {
	// Path is the file the change concerns, relative to the notebook.
	Path string
	// Action is what to do about it.
	Action SyncAction
	// ConflictPath is where the remote version is written, when Action is
	// SyncConflict. It is empty otherwise.
	ConflictPath string
	// Reason says in a few words why, for the commentary and for a dry run.
	Reason string
}

// SyncPlan is everything one synchronization is going to do.
type SyncPlan struct {
	Changes []SyncChange
}

// IsEmpty returns whether the plan would move or record nothing at all.
func (p SyncPlan) IsEmpty() bool {
	return len(p.Changes) == 0
}

// Count returns how many changes carry the given action.
func (p SyncPlan) Count(action SyncAction) int {
	count := 0
	for _, change := range p.Changes {
		if change.Action == action {
			count++
		}
	}
	return count
}

// SyncPlanOpts tunes a plan.
type SyncPlanOpts struct {
	// FirstSync is set when no usable state was left by a previous
	// synchronization, which makes the plan a union of the two sides: nothing
	// is ever deleted, because a file missing from one side cannot be told
	// apart from one which was never there. It is what makes a lost or stale
	// state file harmless.
	FirstSync bool
	// NoPull leaves out everything which would change the notebook.
	NoPull bool
	// NoPush leaves out everything which would change the remote.
	NoPush bool
	// MaxDeletes is the number of deletions beyond which the plan is refused.
	// Zero means no limit.
	MaxDeletes int
	// Force carries on past MaxDeletes.
	Force bool
}

// PlanSync reconciles the two sides against the state the last
// synchronization left behind.
//
// It is a pure function: it reads no file and makes no request, which is what
// lets the whole decision table be tested directly.
func PlanSync(local, remote map[string]SyncEntry, state SyncState, opts SyncPlanOpts) (SyncPlan, error) {
	var plan SyncPlan

	taken := func(path string) bool {
		if _, ok := local[path]; ok {
			return true
		}
		if _, ok := remote[path]; ok {
			return true
		}
		_, ok := state.Files[path]
		return ok
	}

	for _, path := range syncPaths(local, remote, state) {
		localEntry, localPresent := local[path]
		remoteEntry, remotePresent := remote[path]
		record, known := state.Files[path]
		if opts.FirstSync {
			// A state which does not describe this destination is no base at
			// all, and using it would risk deleting files which were never
			// synchronized.
			known = false
			record = SyncRecord{}
		}

		localSide := syncSide(localPresent, known, localEntry.Hash, record.LocalHash)
		remoteSide := syncSide(remotePresent, known, remoteEntry.Hash, record.RemoteETag)

		change, ok := planPath(path, localSide, remoteSide, localEntry, remoteEntry)
		if !ok {
			continue
		}
		if change.Action == SyncConflict {
			change.ConflictPath = syncConflictPath(path, taken)
		}
		plan.Changes = append(plan.Changes, change)
	}

	plan = filterSyncPlan(plan, opts)

	deletions := plan.Count(SyncDeleteLocal) + plan.Count(SyncDeleteRemote)
	if opts.MaxDeletes > 0 && deletions > opts.MaxDeletes && !opts.Force {
		return SyncPlan{}, fmt.Errorf(
			"refusing to synchronize: it would delete %d files, more than the %d allowed by sync.s3.max-deletes - check that this is what you want and run again with --force, or raise the limit",
			deletions, opts.MaxDeletes)
	}

	return plan, nil
}

// syncSideStatus is how one side of a path compares with the last
// synchronization.
type syncSideStatus int

const (
	// syncAbsent: not there now, and not there at the last synchronization.
	syncAbsent syncSideStatus = iota
	// syncSame: there now, and unchanged since the last synchronization.
	syncSame
	// syncModified: there now, and changed since the last synchronization.
	syncModified
	// syncNew: there now, and unknown to the last synchronization.
	syncNew
	// syncDeleted: gone now, but there at the last synchronization.
	syncDeleted
)

// syncSide classifies one side of one path.
func syncSide(present, known bool, hash, recordedHash string) syncSideStatus {
	switch {
	case present && !known:
		return syncNew
	case present && hash == recordedHash && hash != "":
		return syncSame
	case present:
		return syncModified
	case known:
		return syncDeleted
	default:
		return syncAbsent
	}
}

// planPath decides what to do about one path. It returns false when there is
// nothing to do at all.
//
// The whole decision table lives here, and `known` being shared between the
// two sides is what keeps it small: a path is either in the state with both of
// its sides, or in neither, so "unchanged here" can never meet "never seen
// there".
func planPath(path string, localSide, remoteSide syncSideStatus, localEntry, remoteEntry SyncEntry) (SyncChange, bool) {
	change := SyncChange{Path: path}

	switch {
	// Nothing changed anywhere.
	case localSide == syncSame && remoteSide == syncSame:
		return change, false

	// Gone from both sides: only the state still mentions it.
	case localSide == syncDeleted && remoteSide == syncDeleted:
		change.Action = SyncForget
		change.Reason = "deleted on both sides"

	// Changed on one side only.
	case (localSide == syncModified || localSide == syncNew) && (remoteSide == syncSame || remoteSide == syncAbsent):
		change.Action = SyncUpload
		if localSide == syncNew {
			change.Reason = "new here"
		} else {
			change.Reason = "changed here"
		}
	case (localSide == syncSame || localSide == syncAbsent) && (remoteSide == syncModified || remoteSide == syncNew):
		change.Action = SyncDownload
		if remoteSide == syncNew {
			change.Reason = "new on the remote"
		} else {
			change.Reason = "changed on the remote"
		}

	// Changed on both sides. Identical content is not a conflict, only a
	// state which has fallen behind.
	case (localSide == syncModified || localSide == syncNew) && (remoteSide == syncModified || remoteSide == syncNew):
		if syncSameContent(localEntry, remoteEntry) {
			change.Action = SyncRecordOnly
			change.Reason = "changed on both sides to the same content"
		} else {
			change.Action = SyncConflict
			change.Reason = "changed on both sides"
		}

	// Deleted on one side, untouched on the other: the deletion propagates.
	case localSide == syncDeleted && remoteSide == syncSame:
		change.Action = SyncDeleteRemote
		change.Reason = "deleted here"
	case localSide == syncSame && remoteSide == syncDeleted:
		change.Action = SyncDeleteLocal
		change.Reason = "deleted on the remote"

	// Deleted on one side and edited on the other. An edit outranks a
	// deletion, so the file comes back; recording it as synchronized on both
	// sides is what keeps the next run from deleting it again.
	case localSide == syncDeleted && (remoteSide == syncModified || remoteSide == syncNew):
		change.Action = SyncDownload
		change.Reason = "deleted here but changed on the remote, so it comes back"
	case (localSide == syncModified || localSide == syncNew) && remoteSide == syncDeleted:
		change.Action = SyncUpload
		change.Reason = "deleted on the remote but changed here, so it goes back"

	default:
		return change, false
	}

	return change, true
}

// syncSameContent reports whether both sides are known to hold the same
// content. An unknown hash on either side is not an answer, and reports false
// so that the path is treated as a conflict rather than silently resolved the
// wrong way.
func syncSameContent(local, remote SyncEntry) bool {
	if local.ContentHash == "" || remote.ContentHash == "" {
		return false
	}
	return local.ContentHash == remote.ContentHash
}

// filterSyncPlan drops the changes the options rule out.
func filterSyncPlan(plan SyncPlan, opts SyncPlanOpts) SyncPlan {
	out := SyncPlan{}
	for _, change := range plan.Changes {
		switch change.Action {
		case SyncDownload, SyncDeleteLocal:
			if opts.NoPull {
				continue
			}
		case SyncUpload, SyncDeleteRemote:
			if opts.NoPush {
				continue
			}
		case SyncConflict:
			// Resolving a conflict both brings the remote version in and sends
			// the local one out, so half of it is worse than none: it would
			// overwrite one side with no copy of the other kept anywhere.
			if opts.NoPull || opts.NoPush {
				continue
			}
		}
		if opts.FirstSync && (change.Action == SyncDeleteLocal || change.Action == SyncDeleteRemote) {
			// Belt and braces: with no state nothing can be classified as
			// deleted, so this should be unreachable.
			continue
		}
		out.Changes = append(out.Changes, change)
	}
	return out
}

// syncPaths returns every path any side knows about, sorted so that a plan,
// its commentary and its tests are reproducible.
func syncPaths(local, remote map[string]SyncEntry, state SyncState) []string {
	seen := make(map[string]struct{}, len(local)+len(remote)+len(state.Files))
	for path := range local {
		seen[path] = struct{}{}
	}
	for path := range remote {
		seen[path] = struct{}{}
	}
	for path := range state.Files {
		seen[path] = struct{}{}
	}

	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// syncConflictPath is where the remote version of a conflicting file is
// written: `note.md` becomes `note.conflict.md`, and the number grows while
// the name is taken, so that two conflicts in the same file never collide.
//
// The extension is kept last so that the copy is still a note: it is indexed,
// it turns up in `zk list`, and it is synchronized like any other file, which
// is how the other machine ends up with the same pair.
func syncConflictPath(path string, taken func(string) bool) string {
	base, ext := path, ""
	if dot := strings.LastIndex(path, "."); dot > strings.LastIndex(path, "/") && dot != -1 {
		base, ext = path[:dot], path[dot:]
	}

	candidate := base + ".conflict" + ext
	for i := 2; taken(candidate); i++ {
		candidate = fmt.Sprintf("%s.conflict.%d%s", base, i, ext)
	}
	return candidate
}

// SyncContentComparisonPaths returns the paths whose plan depends on whether
// the two sides hold the same content, and so the only ones for which a
// backend need go to the trouble of establishing the remote's content hash.
//
// It exists so that the rule lives next to the decision table it belongs to. A
// backend which resolved every path instead would make one request per note on
// a bucket whose ETags are opaque, which is every bucket encrypted with
// SSE-KMS.
func SyncContentComparisonPaths(local, remote map[string]SyncEntry, state SyncState, firstSync bool) []string {
	var paths []string

	for _, path := range syncPaths(local, remote, state) {
		localEntry, localPresent := local[path]
		remoteEntry, remotePresent := remote[path]
		if !localPresent || !remotePresent {
			continue
		}

		record, known := state.Files[path]
		if firstSync {
			known = false
			record = SyncRecord{}
		}

		localSide := syncSide(localPresent, known, localEntry.Hash, record.LocalHash)
		remoteSide := syncSide(remotePresent, known, remoteEntry.Hash, record.RemoteETag)

		changedHere := localSide == syncModified || localSide == syncNew
		changedThere := remoteSide == syncModified || remoteSide == syncNew
		if changedHere && changedThere {
			paths = append(paths, path)
		}
	}

	return paths
}
