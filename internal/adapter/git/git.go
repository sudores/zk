// Package git runs Git commands in a notebook's repository.
//
// It shells out to the `git` binary rather than linking a Git library, so
// that a notebook is synchronized with exactly the Git the user has
// configured: their credential helper, their SSH keys, their hooks and their
// `.gitconfig` all apply as they would on the command line.
package git

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

// ErrNotARepository is returned when the notebook is not inside a Git
// working tree.
var ErrNotARepository = errors.New("not a Git repository")

// Repo runs Git commands in a working tree.
type Repo struct {
	// Dir is the directory the commands run in. It is the notebook's own
	// directory, which may be a subdirectory of a larger repository: only the
	// notebook's files are then synchronized.
	Dir string
	// Root is the top level of the working tree.
	Root string
	// DryRun prints the commands instead of running them.
	DryRun bool

	stdout io.Writer
	stderr io.Writer
}

// Open returns the repository the given directory belongs to.
func Open(dir string, stdout, stderr io.Writer) (*Repo, error) {
	repo := &Repo{Dir: dir, stdout: stdout, stderr: stderr}

	root, err := repo.output("rev-parse", "--show-toplevel")
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return nil, ErrNotARepository
		}
		return nil, err
	}
	repo.Root = root
	return repo, nil
}

// Run runs a Git command, with its output going to the writers the repository
// was opened with.
func (r *Repo) Run(args ...string) error {
	if r.DryRun {
		fmt.Fprintf(r.stdout, "git %s\n", strings.Join(quote(args), " "))
		return nil
	}

	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	cmd.Stdout = r.stdout
	cmd.Stderr = r.stderr
	if err := cmd.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			// Git has already explained itself on stderr; repeating its exit
			// status would only add noise.
			return fmt.Errorf("git %s failed", args[0])
		}
		return fmt.Errorf("failed to run git: %w", err)
	}
	return nil
}

// output runs a Git command and returns its trimmed standard output. Its
// standard error is left alone, as the callers use it to ask Git a question
// which it is allowed to decline.
func (r *Repo) output(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	out, err := cmd.Output()
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return "", err
		}
		return "", fmt.Errorf("failed to run git: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// HasStagedChanges reports whether anything is staged for the next commit.
func (r *Repo) HasStagedChanges() (bool, error) {
	// --quiet exits with 1 when there is a difference, which is what is being
	// asked about rather than an error.
	cmd := exec.Command("git", "diff", "--cached", "--quiet")
	cmd.Dir = r.Dir
	err := cmd.Run()
	if err == nil {
		return false, nil
	}
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
		return true, nil
	}
	return false, fmt.Errorf("failed to look for staged changes: %w", err)
}

// Upstream returns the branch the current one tracks, or an empty string when
// it tracks none.
func (r *Repo) Upstream() string {
	upstream, err := r.output("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if err != nil {
		return ""
	}
	return upstream
}

// Remotes returns the names of the configured remotes.
func (r *Repo) Remotes() ([]string, error) {
	out, err := r.output("remote")
	if err != nil {
		return nil, fmt.Errorf("failed to list the Git remotes: %w", err)
	}
	if out == "" {
		return []string{}, nil
	}
	return strings.Split(out, "\n"), nil
}

// quote wraps the arguments which need it, so that what a dry run prints can
// be pasted into a shell as it stands.
func quote(args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "" || strings.ContainsAny(arg, " \t'\"$") {
			out = append(out, strconv.Quote(arg))
		} else {
			out = append(out, arg)
		}
	}
	return out
}
