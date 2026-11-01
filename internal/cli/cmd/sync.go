package cmd

import (
	"fmt"

	"github.com/zk-org/zk/internal/adapter/git"
	"github.com/zk-org/zk/internal/cli"
	"github.com/zk-org/zk/internal/cli/spec"
	"github.com/zk-org/zk/internal/core"
)

// Sync synchronizes the notebook with wherever it is kept.
type Sync struct {
	Message string
	NoPull  bool
	NoPush  bool
	DryRun  bool
	Quiet   bool
	Force   bool
}

// SyncSpec describes the `zk sync` command.
func SyncSpec(container *cli.Container) *spec.Command {
	cmd := Sync{}

	return &spec.Command{
		Name:  "sync",
		Short: "Synchronize the notebook with its remote.",
		Long: `Bring in the changes made elsewhere and send out your own.

How a notebook is synchronized is set by ` + "`sync.backend`" + ` in its configuration,
and is Git unless you say otherwise:

    [sync]
    backend = "git"   # or "s3"

With Git, the commands you would run by hand are run for you:

    git add --all
    git commit --message <MESSAGE>
    git pull --rebase
    git push

The notebook must already be a Git repository with a remote, which ` + "`zk`" + ` does not
set up for you: run ` + "`git init`" + ` and ` + "`git remote add`" + ` in it once, and ` + "`zk sync`" + `
afterwards. Nothing is committed when the notes have not changed, and only the
notebook's own files are, leaving the rest of a larger repository alone.

With S3, the notebook is reconciled with a bucket. Unlike Git it has no merge,
so ` + "`zk`" + ` keeps its own record of what each note looked like at the last
synchronization: that is what lets it tell a note you changed from one changed
on another machine, propagate deletions, and keep both versions side by side
when a note really was edited in two places.

The search index in ` + "`.zk/notebook.db`" + ` is never synchronized either way, as it is
rebuilt from the notes and would conflict every time.`,
		Group: GroupNotebook,
		// The notes are about to be synchronized as they are on disk, which
		// the index has no say in.
		SkipAutoIndex: true,
		Flags: []spec.Flag{
			{Name: "message", Short: "m", Placeholder: "MESSAGE", Target: &cmd.Message,
				Help: "Commit message, with the `git` backend. Defaults to `zk sync` and the current date."},
			{Name: "no-pull", Target: &cmd.NoPull,
				Help: "Do not bring in the changes made elsewhere."},
			{Name: "no-push", Target: &cmd.NoPush,
				Help: "Do not send out your own changes."},
			{Name: "dry-run", Short: "n", Target: &cmd.DryRun,
				Help: "Print what would be done, without doing any of it. The plan goes to standard output, so it survives `--quiet`."},
			{Name: "quiet", Short: "q", Target: &cmd.Quiet,
				Help: "Do not print what is being synchronized."},
			{Name: "force", Target: &cmd.Force,
				Help: "Carry on when a safety check would stop the synchronization."},
		},
		Run: func() error { return cmd.Run(container) },
	}
}

func (cmd *Sync) Run(container *cli.Container) error {
	notebook, err := container.CurrentNotebook()
	if err != nil {
		return err
	}

	backend, err := cmd.backend(container, notebook)
	if err != nil {
		return err
	}

	return backend.Sync(core.SyncOpts{
		Message: cmd.Message,
		NoPull:  cmd.NoPull,
		NoPush:  cmd.NoPush,
		DryRun:  cmd.DryRun,
		Quiet:   cmd.Quiet,
		Force:   cmd.Force,
	})
}

// backend builds the synchronization backend the notebook asks for.
func (cmd *Sync) backend(container *cli.Container, notebook *core.Notebook) (core.SyncBackend, error) {
	config := notebook.Config.Sync

	switch config.Backend {
	case core.SyncBackendS3:
		// Said before the backend is built, so that it is seen even when the
		// configuration turns out to be incomplete.
		if cmd.Message != "" && !cmd.Quiet {
			fmt.Fprintf(container.Stderr, "Ignoring --message: it is the `git` backend's commit message, and this notebook synchronizes with S3.\n")
		}
		return newS3Backend(container, notebook)

	default:
		return git.NewBackend(notebook.Path, container.Stdout, container.Stderr)
	}
}
