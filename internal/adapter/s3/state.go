package s3

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zk-org/zk/internal/core"
)

// StateFile is where the record of the last synchronization is kept, relative
// to the notebook.
//
// It lives inside the notebook but is never synchronized: it describes what
// this machine last saw, and would be wrong on any other.
const StateFile = ".zk/sync-state.json"

// stateVersion is bumped when the file's shape changes. A version this zk does
// not know is treated as no state at all, which is safe: the synchronization
// falls back to merging both sides and deletes nothing.
const stateVersion = 1

type persistedState struct {
	Version  int                        `json:"version"`
	Bucket   string                     `json:"bucket"`
	Prefix   string                     `json:"prefix"`
	SyncedAt time.Time                  `json:"synced_at"`
	Files    map[string]persistedRecord `json:"files"`
}

type persistedRecord struct {
	LocalHash  string    `json:"local_hash"`
	LocalSize  int64     `json:"local_size"`
	LocalMTime time.Time `json:"local_mtime"`
	RemoteETag string    `json:"remote_etag"`
	RemoteSize int64     `json:"remote_size"`
}

// LoadState reads the state the last synchronization left behind.
//
// An absent, unreadable or unrecognized file is reported as an error together
// with an empty state, and is not fatal: the caller treats it as a first
// synchronization, which merges the two sides and deletes nothing. That is
// what makes losing this file harmless.
func LoadState(path string) (core.SyncState, error) {
	empty := core.SyncState{Files: map[string]core.SyncRecord{}}

	content, err := os.ReadFile(path)
	if err != nil {
		return empty, err
	}

	var persisted persistedState
	if err := json.Unmarshal(content, &persisted); err != nil {
		return empty, fmt.Errorf("failed to read %s: %w", path, err)
	}
	if persisted.Version != stateVersion {
		return empty, fmt.Errorf("%s was written by a different version of zk", path)
	}

	state := core.SyncState{
		Bucket:   persisted.Bucket,
		Prefix:   persisted.Prefix,
		SyncedAt: persisted.SyncedAt,
		Files:    make(map[string]core.SyncRecord, len(persisted.Files)),
	}
	for path, record := range persisted.Files {
		state.Files[path] = core.SyncRecord{
			LocalHash:  record.LocalHash,
			LocalSize:  record.LocalSize,
			LocalMTime: record.LocalMTime,
			RemoteETag: record.RemoteETag,
			RemoteSize: record.RemoteSize,
		}
	}
	return state, nil
}

// SaveState writes the state, through a temporary file so that an interrupted
// synchronization cannot leave a half-written one behind.
//
// The time of writing is recorded along with it, as that is what later lets a
// scan tell a file it can take on trust from one which may have changed since.
func SaveState(path string, state core.SyncState) error {
	persisted := persistedState{
		Version:  stateVersion,
		Bucket:   state.Bucket,
		Prefix:   state.Prefix,
		SyncedAt: time.Now(),
		Files:    make(map[string]persistedRecord, len(state.Files)),
	}
	for path, record := range state.Files {
		persisted.Files[path] = persistedRecord{
			LocalHash:  record.LocalHash,
			LocalSize:  record.LocalSize,
			LocalMTime: record.LocalMTime,
			RemoteETag: record.RemoteETag,
			RemoteSize: record.RemoteSize,
		}
	}

	content, err := json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}

	temp := path + ".tmp"
	if err := os.WriteFile(temp, content, 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	if err := os.Rename(temp, path); err != nil {
		os.Remove(temp)
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}
