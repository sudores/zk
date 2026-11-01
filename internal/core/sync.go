package core

import "fmt"

// SyncBackend synchronizes a notebook with somewhere else.
//
// The port is deliberately a single method. A Git remote and an object store
// have almost nothing in common beyond "make both sides match", so trying to
// give `Pull`, `Push` or `Commit` a meaning for both would only produce an
// interface neither can honour.
type SyncBackend interface {
	// Sync performs one synchronization.
	Sync(opts SyncOpts) error
}

// SyncOpts are the options a synchronization honours. Not every backend can
// honour every one: Message is Git's alone, and Force is only meaningful to a
// backend which deletes files on its own initiative.
type SyncOpts struct {
	// Message is the Git commit message.
	Message string
	// NoPull keeps the changes made elsewhere from being brought in.
	NoPull bool
	// NoPush keeps the local changes from being sent out.
	NoPush bool
	// DryRun reports what would be done, without doing any of it.
	DryRun bool
	// Quiet silences the running commentary, leaving only errors.
	Quiet bool
	// Force carries on when a safety check would otherwise stop the
	// synchronization, such as the limit on the number of deletions.
	Force bool
}

// SyncBackendName identifies a synchronization backend, as named by the
// `sync.backend` setting.
type SyncBackendName string

const (
	// SyncBackendGit synchronizes through the notebook's Git repository.
	SyncBackendGit SyncBackendName = "git"
	// SyncBackendS3 synchronizes through an S3 bucket.
	SyncBackendS3 SyncBackendName = "s3"
)

// syncBackendFromString returns the backend named by a configuration value.
func syncBackendFromString(s string) (SyncBackendName, error) {
	switch s {
	case "git":
		return SyncBackendGit, nil
	case "s3":
		return SyncBackendS3, nil
	default:
		return "", fmt.Errorf("%s: unknown sync backend - may be git or s3", s)
	}
}
