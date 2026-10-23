package cmd

import (
	"errors"
	"fmt"
	"io"

	"github.com/zk-org/zk/internal/adapter/fzf"
	"github.com/zk-org/zk/internal/cli"
	"github.com/zk-org/zk/internal/cli/spec"
	"github.com/zk-org/zk/internal/util/strings"
)

// List displays notes matching a set of criteria.
type List struct {
	Format     string
	Link       bool
	Header     string
	Footer     string
	Delimiter  string
	Delimiter0 bool
	NoPager    bool
	Quiet      bool
	cli.Filtering
}

// ListSpec describes the `zk list` command.
func ListSpec(container *cli.Container) *spec.Command {
	cmd := List{}
	arg := cmd.PathArg()
	arg.Complete = completeNotePaths(container)

	flags := []spec.Flag{
		{Name: "format", Short: "f", Group: spec.GroupFormat, Placeholder: "TEMPLATE", Target: &cmd.Format,
			Help:     "Pretty print the list using a custom template or one of the predefined formats: oneline, short, medium, long, full, json, jsonl, uri.",
			Complete: spec.Values("oneline", "short", "medium", "long", "full", "json", "jsonl", "uri", "path", "link")},
		{Name: "link", Group: spec.GroupFormat, Target: &cmd.Link,
			Help: "Print the zk:// URI of each note (shorthand for --format uri)."},
		{Name: "header", Group: spec.GroupFormat, Target: &cmd.Header,
			Help: "Arbitrary text printed at the start of the list."},
		// The default of --footer is the two characters `\n`, which
		// ExpandWhitespaceLiterals turns into a newline, while --delimiter
		// defaults to a real one. Both end up as a newline, but the help
		// shows them differently, as it always has. Making them consistent
		// would change the documented output of `zk list --help`.
		{Name: "footer", Group: spec.GroupFormat, Default: `\n`, Target: &cmd.Footer,
			Help: "Arbitrary text printed at the end of the list."},
		{Name: "delimiter", Short: "d", Group: spec.GroupFormat, Default: "\n", Target: &cmd.Delimiter,
			Help: "Print notes delimited by the given separator."},
		{Name: "delimiter0", Short: "0", Group: spec.GroupFormat, Target: &cmd.Delimiter0,
			Help: "Print notes delimited by ASCII NUL characters. This is useful when used in conjunction with `xargs -0`."},
		{Name: "no-pager", Short: "P", Group: spec.GroupFormat, Target: &cmd.NoPager,
			Help: "Do not pipe output into a pager."},
		{Name: "quiet", Short: "q", Group: spec.GroupFormat, Target: &cmd.Quiet,
			Help: "Do not print the total number of notes found."},
	}

	return &spec.Command{
		Name:  "list",
		Short: "List notes matching the given criteria.",
		Group: GroupNotes,
		Args:  []spec.Arg{arg},
		Flags: append(flags, filteringFlags(container, &cmd.Filtering)...),
		Run:   func() error { return cmd.Run(container) },
	}
}

func (cmd *List) Run(container *cli.Container) error {
	if cmd.Link {
		if cmd.Format != "" {
			return errors.New("--link and --format can't be used together")
		}
		cmd.Format = "uri"
	}

	cmd.Header = strings.ExpandWhitespaceLiterals(cmd.Header)
	cmd.Footer = strings.ExpandWhitespaceLiterals(cmd.Footer)
	cmd.Delimiter = strings.ExpandWhitespaceLiterals(cmd.Delimiter)

	if cmd.Delimiter0 {
		if cmd.Delimiter != "\n" {
			return errors.New("--delimiter and --delimiter0 can't be used together")
		}
		if cmd.Header != "" {
			return errors.New("--header and --delimiter0 can't be used together")
		}
		if cmd.Footer != "\n" {
			return errors.New("--footer and --delimiter0 can't be used together")
		}

		cmd.Delimiter = "\x00"
		cmd.Footer = "\x00"
	}

	if cmd.Format == "json" || cmd.Format == "jsonl" {
		if cmd.Header != "" {
			return errors.New("--header can't be used with JSON format")
		}
		if cmd.Footer != "\n" {
			return errors.New("--footer can't be used with JSON format")
		}
		if cmd.Delimiter != "\n" {
			return errors.New("--delimiter can't be used with JSON format")
		}

		switch cmd.Format {
		case "json":
			cmd.Delimiter = ","
			cmd.Header = "["
			cmd.Footer = "]\n"

		case "jsonl":
			// > The last character in the file may be a line separator, and it
			// > will be treated the same as if there was no line separator
			// > present.
			// > https://jsonlines.org/
			cmd.Footer = "\n"
		}
	}

	notebook, err := container.CurrentNotebook()
	if err != nil {
		return err
	}

	format, err := notebook.NewNoteFormatter(cmd.noteTemplate())
	if err != nil {
		return err
	}

	findOpts, err := cmd.NewNoteFindOpts(notebook)
	if err != nil {
		return fmt.Errorf("incorrect criteria: %w", err)
	}

	notes, err := notebook.FindNotes(findOpts)
	if err != nil {
		return err
	}

	filter := container.NewNoteFilter(fzf.NoteFilterOpts{
		Interactive:  cmd.Interactive,
		AlwaysFilter: false,
		NotebookDir:  notebook.Path,
	})

	notes, err = filter.Apply(notes)
	if err != nil {
		if err == fzf.ErrCancelled {
			return nil
		}
		return err
	}

	count := len(notes)
	if count > 0 {
		err = container.Paginate(cmd.NoPager, func(out io.Writer) error {
			if cmd.Header != "" {
				fmt.Fprint(out, cmd.Header)
			}
			for i, note := range notes {
				if i > 0 {
					fmt.Fprint(out, cmd.Delimiter)
				}

				ft, err := format(note)
				if err != nil {
					return err
				}
				fmt.Fprint(out, ft)
			}
			if cmd.Footer != "" {
				fmt.Fprint(out, cmd.Footer)
			}

			return nil
		})
	}

	if err == nil && !cmd.Quiet {
		fmt.Fprintf(container.Stderr, "\nFound %d %s\n", count, strings.Pluralize("note", count))
	}

	return err
}

func (cmd *List) noteTemplate() string {
	format := cmd.Format
	if format == "" {
		format = "short"
	}

	templ, ok := defaultNoteFormats[format]
	if !ok {
		templ = strings.ExpandWhitespaceLiterals(format)
	}

	return templ
}

var defaultNoteFormats = map[string]string{
	"json":  `{{json .}}`,
	"jsonl": `{{json .}}`,
	"path":  `{{path}}`,
	"link":  `{{link}}`,
	"uri":   `{{uri}}`,

	"oneline": `{{style "title" title}} {{style "path" path}} ({{format-date created "elapsed"}})`,

	"short": `{{style "title" title}} {{style "path" path}} ({{format-date created "elapsed"}})

{{list snippets}}`,

	"medium": `{{style "title" title}} {{style "path" path}}
Created: {{format-date created "short"}}

{{list snippets}}`,

	"long": `{{style "title" title}} {{style "path" path}}
Created: {{format-date created "short"}}
Modified: {{format-date modified "short"}}

{{list snippets}}`,

	"full": `{{style "title" title}} {{style "path" path}}
Created: {{format-date created "short"}}
Modified: {{format-date modified "short"}}
Tags: {{join tags ", "}}

{{prepend "  " body}}
`,
}
