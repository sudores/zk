package s3

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zk-org/zk/internal/core"
	"github.com/zk-org/zk/internal/util/test/assert"
)

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".zk", "sync-state.json")

	// Truncated to the second, as JSON keeps the time but a comparison should
	// not depend on the clock's resolution.
	modified := time.Now().UTC().Truncate(time.Second)
	state := core.SyncState{
		Bucket: "notes",
		Prefix: "notebook/",
		Files: map[string]core.SyncRecord{
			"hello.md": {
				LocalHash:  "d41d8cd98f00b204e9800998ecf8427e",
				LocalSize:  12,
				LocalMTime: modified,
				RemoteETag: "d41d8cd98f00b204e9800998ecf8427e",
				RemoteSize: 12,
			},
		},
	}

	// The parent directory is created along the way, as `.zk` may not exist in
	// a notebook which has never been indexed.
	assert.Nil(t, SaveState(path, state))

	loaded, err := LoadState(path)
	assert.Nil(t, err)
	assert.Equal(t, loaded.Bucket, state.Bucket)
	assert.Equal(t, loaded.Prefix, state.Prefix)
	assert.Equal(t, loaded.Files, state.Files)

	// The time of writing is recorded, as that is what later lets a scan tell
	// a file it can take on trust from one which may have changed since.
	if loaded.SyncedAt.IsZero() {
		t.Error("expected the state to record when it was written")
	}
}

// TestLoadStateMissing checks the ordinary case of a notebook which has never
// been synchronized: it is reported as absent, which the caller reads as a
// first synchronization rather than as a failure.
func TestLoadStateMissing(t *testing.T) {
	state, err := LoadState(filepath.Join(t.TempDir(), "sync-state.json"))

	assert.True(t, errors.Is(err, os.ErrNotExist))
	assert.Equal(t, len(state.Files), 0)
}

// TestLoadStateCorrupt checks that an unreadable state is a fresh start and
// not an impasse. It comes back empty, so the synchronization merges both
// sides and deletes nothing.
func TestLoadStateCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync-state.json")
	assert.Nil(t, os.WriteFile(path, []byte("{ this is not JSON"), 0o644))

	state, err := LoadState(path)
	assert.NotNil(t, err)
	assert.Equal(t, len(state.Files), 0)
}

// TestLoadStateFromAnotherVersion checks the same for a file this zk does not
// understand, rather than one which is damaged.
func TestLoadStateFromAnotherVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync-state.json")
	assert.Nil(t, os.WriteFile(path, []byte(`{"version": 99, "bucket": "notes"}`), 0o644))

	state, err := LoadState(path)
	assert.Err(t, err, "written by a different version of zk")
	assert.Equal(t, len(state.Files), 0)
}

// TestSaveStateLeavesNoTemporaryFile checks that the atomic write cleans up
// after itself, as the temporary file sits inside the notebook.
func TestSaveStateLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sync-state.json")

	assert.Nil(t, SaveState(path, core.SyncState{Bucket: "notes", Files: map[string]core.SyncRecord{}}))

	entries, err := os.ReadDir(dir)
	assert.Nil(t, err)
	assert.Equal(t, len(entries), 1)
	assert.Equal(t, entries[0].Name(), "sync-state.json")
}

func TestTrimETag(t *testing.T) {
	assert.Equal(t, TrimETag(`"d41d8cd98f00b204e9800998ecf8427e"`), "d41d8cd98f00b204e9800998ecf8427e")
	assert.Equal(t, TrimETag("d41d8cd98f00b204e9800998ecf8427e"), "d41d8cd98f00b204e9800998ecf8427e")
	assert.Equal(t, TrimETag(""), "")
}

// TestIsMD5 covers the question the whole ETag handling turns on: does this
// ETag say anything about the content?
func TestIsMD5(t *testing.T) {
	assert.True(t, IsMD5("d41d8cd98f00b204e9800998ecf8427e"))
	assert.True(t, IsMD5("D41D8CD98F00B204E9800998ECF8427E"))
	// A multipart upload's ETag.
	assert.False(t, IsMD5("d41d8cd98f00b204e9800998ecf8427e-2"))
	// Too short, and not hexadecimal.
	assert.False(t, IsMD5("d41d8cd9"))
	assert.False(t, IsMD5("z41d8cd98f00b204e9800998ecf8427e"))
	assert.False(t, IsMD5(""))
}

func TestNormalizePrefix(t *testing.T) {
	assert.Equal(t, normalizePrefix(""), "")
	assert.Equal(t, normalizePrefix("/"), "")
	assert.Equal(t, normalizePrefix("notebook"), "notebook/")
	assert.Equal(t, normalizePrefix("/notebook/"), "notebook/")
	assert.Equal(t, normalizePrefix("a/b"), "a/b/")
}
