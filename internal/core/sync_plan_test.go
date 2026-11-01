package core

import (
	"testing"

	"github.com/zk-org/zk/internal/util/test/assert"
)

// lo builds a local entry. Locally the change-detection token and the content
// hash are the same thing: the MD5 of the file.
func lo(hash string) SyncEntry {
	return SyncEntry{Hash: hash, ContentHash: hash, Size: int64(len(hash))}
}

// re builds a remote entry whose ETag is the content's MD5, which is the usual
// case: one part, and either unencrypted or SSE-S3.
func re(hash string) SyncEntry {
	return SyncEntry{Hash: hash, ContentHash: hash, Size: int64(len(hash))}
}

// reOpaque builds a remote entry whose ETag says nothing about the content,
// as under SSE-KMS or after a multipart upload.
func reOpaque(etag string) SyncEntry {
	return SyncEntry{Hash: etag, ContentHash: ""}
}

// synced builds the state record of a file both sides agreed on.
func synced(hash string) SyncRecord {
	return SyncRecord{LocalHash: hash, RemoteETag: hash}
}

func stateOf(files map[string]SyncRecord) SyncState {
	return SyncState{Bucket: "b", Prefix: "p", Files: files}
}

// change is the part of a SyncChange the tests pin down; the Reason is prose
// and is left out on purpose.
type change struct {
	Path     string
	Action   SyncAction
	Conflict string
}

func changesOf(plan SyncPlan) []change {
	out := []change{}
	for _, c := range plan.Changes {
		out = append(out, change{Path: c.Path, Action: c.Action, Conflict: c.ConflictPath})
	}
	return out
}

// TestPlanSync walks the whole decision table, one row per case.
func TestPlanSync(t *testing.T) {
	tests := []struct {
		name   string
		local  map[string]SyncEntry
		remote map[string]SyncEntry
		state  SyncState
		opts   SyncPlanOpts
		want   []change
	}{
		{
			name:   "nothing changed on either side",
			local:  map[string]SyncEntry{"a.md": lo("1")},
			remote: map[string]SyncEntry{"a.md": re("1")},
			state:  stateOf(map[string]SyncRecord{"a.md": synced("1")}),
			want:   []change{},
		},
		{
			name:   "changed here only",
			local:  map[string]SyncEntry{"a.md": lo("2")},
			remote: map[string]SyncEntry{"a.md": re("1")},
			state:  stateOf(map[string]SyncRecord{"a.md": synced("1")}),
			want:   []change{{Path: "a.md", Action: SyncUpload}},
		},
		{
			name:   "new here only",
			local:  map[string]SyncEntry{"a.md": lo("1")},
			remote: map[string]SyncEntry{},
			state:  stateOf(map[string]SyncRecord{}),
			want:   []change{{Path: "a.md", Action: SyncUpload}},
		},
		{
			name:   "changed on the remote only",
			local:  map[string]SyncEntry{"a.md": lo("1")},
			remote: map[string]SyncEntry{"a.md": re("2")},
			state:  stateOf(map[string]SyncRecord{"a.md": synced("1")}),
			want:   []change{{Path: "a.md", Action: SyncDownload}},
		},
		{
			name:   "new on the remote only",
			local:  map[string]SyncEntry{},
			remote: map[string]SyncEntry{"a.md": re("1")},
			state:  stateOf(map[string]SyncRecord{}),
			want:   []change{{Path: "a.md", Action: SyncDownload}},
		},
		{
			name:   "changed on both sides to the same content moves nothing",
			local:  map[string]SyncEntry{"a.md": lo("2")},
			remote: map[string]SyncEntry{"a.md": re("2")},
			state:  stateOf(map[string]SyncRecord{"a.md": synced("1")}),
			want:   []change{{Path: "a.md", Action: SyncRecordOnly}},
		},
		{
			name:   "changed on both sides to different content conflicts",
			local:  map[string]SyncEntry{"a.md": lo("2")},
			remote: map[string]SyncEntry{"a.md": re("3")},
			state:  stateOf(map[string]SyncRecord{"a.md": synced("1")}),
			want:   []change{{Path: "a.md", Action: SyncConflict, Conflict: "a.conflict.md"}},
		},
		{
			name:   "an opaque remote ETag cannot prove equality, so it conflicts",
			local:  map[string]SyncEntry{"a.md": lo("2")},
			remote: map[string]SyncEntry{"a.md": reOpaque("opaque")},
			state:  stateOf(map[string]SyncRecord{"a.md": synced("1")}),
			want:   []change{{Path: "a.md", Action: SyncConflict, Conflict: "a.conflict.md"}},
		},
		{
			name:   "deleted here propagates to the remote",
			local:  map[string]SyncEntry{},
			remote: map[string]SyncEntry{"a.md": re("1")},
			state:  stateOf(map[string]SyncRecord{"a.md": synced("1")}),
			want:   []change{{Path: "a.md", Action: SyncDeleteRemote}},
		},
		{
			name:   "deleted on the remote propagates here",
			local:  map[string]SyncEntry{"a.md": lo("1")},
			remote: map[string]SyncEntry{},
			state:  stateOf(map[string]SyncRecord{"a.md": synced("1")}),
			want:   []change{{Path: "a.md", Action: SyncDeleteLocal}},
		},
		{
			name:   "deleted here but changed on the remote comes back",
			local:  map[string]SyncEntry{},
			remote: map[string]SyncEntry{"a.md": re("2")},
			state:  stateOf(map[string]SyncRecord{"a.md": synced("1")}),
			want:   []change{{Path: "a.md", Action: SyncDownload}},
		},
		{
			name:   "deleted on the remote but changed here goes back",
			local:  map[string]SyncEntry{"a.md": lo("2")},
			remote: map[string]SyncEntry{},
			state:  stateOf(map[string]SyncRecord{"a.md": synced("1")}),
			want:   []change{{Path: "a.md", Action: SyncUpload}},
		},
		{
			name:   "deleted on both sides is only forgotten",
			local:  map[string]SyncEntry{},
			remote: map[string]SyncEntry{},
			state:  stateOf(map[string]SyncRecord{"a.md": synced("1")}),
			want:   []change{{Path: "a.md", Action: SyncForget}},
		},
		{
			name:   "a conflict avoids a name already taken",
			local:  map[string]SyncEntry{"a.md": lo("2"), "a.conflict.md": lo("9")},
			remote: map[string]SyncEntry{"a.md": re("3"), "a.conflict.md": re("9")},
			state:  stateOf(map[string]SyncRecord{"a.md": synced("1"), "a.conflict.md": synced("9")}),
			want:   []change{{Path: "a.md", Action: SyncConflict, Conflict: "a.conflict.2.md"}},
		},
		{
			name:   "a file with no extension still gets a conflict copy",
			local:  map[string]SyncEntry{"README": lo("2")},
			remote: map[string]SyncEntry{"README": re("3")},
			state:  stateOf(map[string]SyncRecord{"README": synced("1")}),
			want:   []change{{Path: "README", Action: SyncConflict, Conflict: "README.conflict"}},
		},
		{
			name:   "a dot in a directory name is not an extension",
			local:  map[string]SyncEntry{"a.d/note": lo("2")},
			remote: map[string]SyncEntry{"a.d/note": re("3")},
			state:  stateOf(map[string]SyncRecord{"a.d/note": synced("1")}),
			want:   []change{{Path: "a.d/note", Action: SyncConflict, Conflict: "a.d/note.conflict"}},
		},
		{
			name:   "the plan is sorted by path",
			local:  map[string]SyncEntry{"c.md": lo("1"), "a.md": lo("1"), "b.md": lo("1")},
			remote: map[string]SyncEntry{},
			state:  stateOf(map[string]SyncRecord{}),
			want: []change{
				{Path: "a.md", Action: SyncUpload},
				{Path: "b.md", Action: SyncUpload},
				{Path: "c.md", Action: SyncUpload},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := PlanSync(test.local, test.remote, test.state, test.opts)
			assert.Nil(t, err)
			assert.Equal(t, changesOf(plan), test.want)
		})
	}
}

// TestPlanSyncFirstSync covers the mode which makes a lost or stale state file
// harmless: the two sides are merged, and nothing is ever deleted.
func TestPlanSyncFirstSync(t *testing.T) {
	tests := []struct {
		name   string
		local  map[string]SyncEntry
		remote map[string]SyncEntry
		state  SyncState
		want   []change
	}{
		{
			name:   "local-only files are uploaded",
			local:  map[string]SyncEntry{"a.md": lo("1")},
			remote: map[string]SyncEntry{},
			want:   []change{{Path: "a.md", Action: SyncUpload}},
		},
		{
			name:   "remote-only files are downloaded",
			local:  map[string]SyncEntry{},
			remote: map[string]SyncEntry{"a.md": re("1")},
			want:   []change{{Path: "a.md", Action: SyncDownload}},
		},
		{
			name:   "the same content on both sides moves nothing",
			local:  map[string]SyncEntry{"a.md": lo("1")},
			remote: map[string]SyncEntry{"a.md": re("1")},
			want:   []change{{Path: "a.md", Action: SyncRecordOnly}},
		},
		{
			name:   "different content on both sides conflicts, with no base to judge by",
			local:  map[string]SyncEntry{"a.md": lo("1")},
			remote: map[string]SyncEntry{"a.md": re("2")},
			want:   []change{{Path: "a.md", Action: SyncConflict, Conflict: "a.conflict.md"}},
		},
		{
			name:   "a leftover state is ignored, so nothing is deleted",
			local:  map[string]SyncEntry{"a.md": lo("1")},
			remote: map[string]SyncEntry{},
			state:  stateOf(map[string]SyncRecord{"a.md": synced("1"), "gone.md": synced("7")}),
			want:   []change{{Path: "a.md", Action: SyncUpload}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := PlanSync(test.local, test.remote, test.state, SyncPlanOpts{FirstSync: true})
			assert.Nil(t, err)
			assert.Equal(t, changesOf(plan), test.want)
		})
	}
}

// TestPlanSyncNoPullNoPush checks that half a synchronization is still safe.
func TestPlanSyncNoPullNoPush(t *testing.T) {
	local := map[string]SyncEntry{"up.md": lo("2"), "down.md": lo("1"), "delremote.md": lo("1")}
	remote := map[string]SyncEntry{"up.md": re("1"), "down.md": re("2"), "dellocal.md": re("1")}
	state := stateOf(map[string]SyncRecord{
		"up.md":        synced("1"),
		"down.md":      synced("1"),
		"dellocal.md":  synced("1"),
		"delremote.md": synced("1"),
	})
	// Each name says where the file was deleted: delremote.md is gone from the
	// remote, so the deletion travels inwards, and dellocal.md is the mirror
	// image.
	delete(remote, "delremote.md")
	delete(local, "dellocal.md")

	t.Run("no-pull keeps the notebook untouched", func(t *testing.T) {
		plan, err := PlanSync(local, remote, state, SyncPlanOpts{NoPull: true})
		assert.Nil(t, err)
		assert.Equal(t, changesOf(plan), []change{
			{Path: "dellocal.md", Action: SyncDeleteRemote},
			{Path: "up.md", Action: SyncUpload},
		})
	})

	t.Run("no-push keeps the remote untouched", func(t *testing.T) {
		plan, err := PlanSync(local, remote, state, SyncPlanOpts{NoPush: true})
		assert.Nil(t, err)
		assert.Equal(t, changesOf(plan), []change{
			{Path: "delremote.md", Action: SyncDeleteLocal},
			{Path: "down.md", Action: SyncDownload},
		})
	})

	t.Run("a conflict needs both directions, so half a sync skips it", func(t *testing.T) {
		conflictLocal := map[string]SyncEntry{"a.md": lo("2")}
		conflictRemote := map[string]SyncEntry{"a.md": re("3")}
		conflictState := stateOf(map[string]SyncRecord{"a.md": synced("1")})

		for _, opts := range []SyncPlanOpts{{NoPull: true}, {NoPush: true}} {
			plan, err := PlanSync(conflictLocal, conflictRemote, conflictState, opts)
			assert.Nil(t, err)
			assert.Equal(t, changesOf(plan), []change{})
		}
	})
}

// TestPlanSyncDeleteGuard checks that a stale state cannot quietly empty a
// notebook.
func TestPlanSyncDeleteGuard(t *testing.T) {
	state := stateOf(map[string]SyncRecord{})
	for _, path := range []string{"a.md", "b.md", "c.md", "d.md"} {
		state.Files[path] = synced("1")
	}
	remote := map[string]SyncEntry{
		"a.md": re("1"), "b.md": re("1"), "c.md": re("1"), "d.md": re("1"),
	}

	_, err := PlanSync(map[string]SyncEntry{}, remote, state, SyncPlanOpts{MaxDeletes: 3})
	assert.Err(t, err, "it would delete 4 files")
	assert.Err(t, err, "--force")

	plan, err := PlanSync(map[string]SyncEntry{}, remote, state, SyncPlanOpts{MaxDeletes: 3, Force: true})
	assert.Nil(t, err)
	assert.Equal(t, plan.Count(SyncDeleteRemote), 4)

	plan, err = PlanSync(map[string]SyncEntry{}, remote, state, SyncPlanOpts{MaxDeletes: 4})
	assert.Nil(t, err)
	assert.Equal(t, plan.Count(SyncDeleteRemote), 4)

	plan, err = PlanSync(map[string]SyncEntry{}, remote, state, SyncPlanOpts{MaxDeletes: 0})
	assert.Nil(t, err)
	assert.Equal(t, plan.Count(SyncDeleteRemote), 4)
}
