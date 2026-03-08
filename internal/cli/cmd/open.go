package cmd

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/zk-org/zk/internal/cli"
	"github.com/zk-org/zk/internal/core"
)

// Open opens a note identified by a zk:// URI in the default editor.
type Open struct {
	URI string `arg help:"zk:// URI of the note to open (e.g. zk://notebook/path/to/note.md)."`
}

func (cmd *Open) Run(container *cli.Container) error {
	u, err := url.Parse(cmd.URI)
	if err != nil || u.Scheme != "zk" || u.Host == "" {
		return fmt.Errorf("%s: not a valid zk:// URI", cmd.URI)
	}

	notebook, err := container.CurrentNotebook()
	if err != nil {
		return err
	}

	notebookName := filepath.Base(notebook.Path)
	if u.Host != notebookName {
		return fmt.Errorf(
			"URI notebook %q does not match current notebook %q; use --notebook-dir or ZK_NOTEBOOK_DIR to select the right notebook",
			u.Host, notebookName,
		)
	}

	noteID := strings.TrimPrefix(u.Path, "/")
	note, err := notebook.FindNote(core.NoteFindOpts{
		IncludeHrefs:      []string{noteID},
		AllowPartialHrefs: true,
	})
	if err != nil {
		return err
	}
	if note == nil {
		return fmt.Errorf("%s: note not found in notebook %s", noteID, notebookName)
	}

	absPath := filepath.Join(notebook.Path, note.Path)
	editor, err := container.NewNoteEditor(notebook)
	if err != nil {
		return err
	}
	return editor.Open(absPath)
}
