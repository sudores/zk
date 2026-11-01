package git

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/zk-org/zk/internal/core"
)

// excludedFromSync are the files a synchronization keeps out of the commits it
// makes. The index is derived from the notes and is rewritten constantly, so
// committing it would make every sync a conflict waiting to happen.
var excludedFromSync = []string{
	":(exclude).zk/notebook.db",
	":(exclude).zk/notebook.db-wal",
	":(exclude).zk/notebook.db-shm",
}

// Backend synchronizes a notebook with its remote Git repository.
//
// It implements core.SyncBackend by running the commands the user would run by
// hand, in this order:
//
//	git add --all
//	git commit --message <MESSAGE>
//	git pull --rebase
//	git push
type Backend struct {
	repo   *Repo
	stderr io.Writer
	quiet  bool
}

// NewBackend opens the repository the notebook belongs to.
func NewBackend(path string, stdout, stderr io.Writer) (*Backend, error) {
	repo, err := Open(path, stdout, stderr)
	if err != nil {
		if errors.Is(err, ErrNotARepository) {
			return nil, fmt.Errorf("%s is not a Git repository: run `git init` and `git remote add` in it to synchronize it", path)
		}
		return nil, err
	}
	return &Backend{repo: repo, stderr: stderr}, nil
}

// Sync implements core.SyncBackend.
func (b *Backend) Sync(opts core.SyncOpts) error {
	b.quiet = opts.Quiet
	b.repo.DryRun = opts.DryRun

	// A missing remote is reported before anything is committed, as the whole
	// point of the command is to reach one.
	upstream := b.repo.Upstream()
	remotes, err := b.repo.Remotes()
	if err != nil {
		return err
	}
	if len(remotes) == 0 && !(opts.NoPull && opts.NoPush) {
		return errors.New("the notebook's Git repository has no remote to synchronize with: add one with `git remote add origin <URL>`")
	}

	// Worth saying, as only part of the repository is being synchronized.
	if b.repo.Root != "" && b.repo.Root != b.repo.Dir {
		b.logf("The notebook is a subdirectory of the Git repository at %s: only its own files are synchronized.\n", b.repo.Root)
	}

	if err := b.commit(opts); err != nil {
		return err
	}

	if !opts.NoPull {
		if upstream == "" {
			b.logf("Nothing to pull: the current branch tracks no remote branch yet.\n")
		} else {
			b.logf("Pulling from %s...\n", upstream)
			if err := b.repo.Run(b.gitArgs("pull", "--rebase")...); err != nil {
				return err
			}
		}
	}

	if !opts.NoPush {
		args := b.gitArgs("push")
		if upstream == "" {
			// The branch is pushed to the first remote and set to track it,
			// which is what `git push` would ask the user to do by hand.
			b.logf("Pushing to %s, and tracking it from now on...\n", remotes[0])
			args = append(args, "--set-upstream", remotes[0], "HEAD")
		} else {
			b.logf("Pushing to %s...\n", upstream)
		}
		if err := b.repo.Run(args...); err != nil {
			return err
		}
	}

	return nil
}

// commit stages the notebook's files and commits them, unless nothing
// changed.
func (b *Backend) commit(opts core.SyncOpts) error {
	add := append([]string{"add", "--all", "--", "."}, excludedFromSync...)
	if err := b.repo.Run(add...); err != nil {
		return err
	}

	// A dry run stages nothing, so there is never anything to commit and
	// reporting it would be misleading.
	if !opts.DryRun {
		staged, err := b.repo.HasStagedChanges()
		if err != nil {
			return err
		}
		if !staged {
			b.logf("Nothing to commit: the notes have not changed.\n")
			return nil
		}
	}

	b.logf("Committing the changes...\n")
	return b.repo.Run(b.gitArgs("commit", "--message", commitMessage(opts.Message))...)
}

func commitMessage(message string) string {
	if message != "" {
		return message
	}
	return "zk sync: " + time.Now().Format("2006-01-02 15:04:05")
}

// gitArgs passes --quiet on to Git, so that `zk sync --quiet` is quiet as a
// whole rather than only in what zk prints itself.
func (b *Backend) gitArgs(args ...string) []string {
	if b.quiet {
		return append(args, "--quiet")
	}
	return args
}

func (b *Backend) logf(format string, args ...any) {
	if !b.quiet {
		fmt.Fprintf(b.stderr, format, args...)
	}
}
