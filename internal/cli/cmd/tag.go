package cmd

import (
	"errors"
	"fmt"
	"io"

	"github.com/zk-org/zk/internal/cli"
	"github.com/zk-org/zk/internal/cli/spec"
	"github.com/zk-org/zk/internal/core"
	"github.com/zk-org/zk/internal/util/strings"
)

// TagList lists all the note tags.
type TagList struct {
	Format     string
	Header     string
	Footer     string
	Delimiter  string
	Delimiter0 bool
	NoPager    bool
	Quiet      bool
	Sort       []string
}

// TagSpec describes the `zk tag` command and its subcommands.
func TagSpec(container *cli.Container) *spec.Command {
	return &spec.Command{
		Name:  "tag",
		Short: "Manage the note tags.",
		Group: GroupNotes,
		Children: []*spec.Command{
			tagListSpec(container),
		},
		DefaultChild: "list",
	}
}

func tagListSpec(container *cli.Container) *spec.Command {
	cmd := TagList{}

	return &spec.Command{
		Name:  "list",
		Short: "List all the note tags.",
		Flags: []spec.Flag{
			{Name: "format", Short: "f", Group: spec.GroupFormat, Placeholder: "TEMPLATE", Target: &cmd.Format,
				Help:     "Pretty print the list using a custom template or one of the predefined formats: name, full, json, jsonl.",
				Complete: spec.Values("name", "full", "json", "jsonl")},
			{Name: "header", Group: spec.GroupFormat, Target: &cmd.Header,
				Help: "Arbitrary text printed at the start of the list."},
			// The default of --footer is the two characters `\n`, which
			// ExpandWhitespaceLiterals turns into a newline, while --delimiter
			// defaults to a real one. Both end up as a newline, but the help
			// shows them differently, as it always has. Making them consistent
			// would change the documented output of `zk tag list --help`.
			{Name: "footer", Group: spec.GroupFormat, Default: `\n`, Target: &cmd.Footer,
				Help: "Arbitrary text printed at the end of the list."},
			{Name: "delimiter", Short: "d", Group: spec.GroupFormat, Default: "\n", Target: &cmd.Delimiter,
				Help: "Print tags delimited by the given separator."},
			{Name: "delimiter0", Short: "0", Group: spec.GroupFormat, Target: &cmd.Delimiter0,
				Help: "Print tags delimited by ASCII NUL characters. This is useful when used in conjunction with `xargs -0`."},
			{Name: "no-pager", Short: "P", Group: spec.GroupFormat, Target: &cmd.NoPager,
				Help: "Do not pipe output into a pager."},
			{Name: "quiet", Short: "q", Group: spec.GroupFormat, Target: &cmd.Quiet,
				Help: "Do not print the total number of tags found."},

			{Name: "sort", Short: "s", Group: spec.GroupSort, Placeholder: "TERM", Target: &cmd.Sort,
				Help:     "Order the tags by the given criterion.",
				Complete: completeTagSortTerms()},
		},
		Run: func() error { return cmd.Run(container) },
	}
}

func (cmd *TagList) Run(container *cli.Container) error {
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

	format, err := notebook.NewCollectionFormatter(cmd.tagTemplate())
	if err != nil {
		return err
	}

	sorters, err := core.CollectionSortersFromStrings(cmd.Sort)
	if err != nil {
		return err
	}

	tags, err := notebook.FindCollections(core.CollectionKindTag, sorters)
	if err != nil {
		return err
	}

	count := len(tags)
	if count > 0 {
		err = container.Paginate(cmd.NoPager, func(out io.Writer) error {
			if cmd.Header != "" {
				fmt.Fprint(out, cmd.Header)
			}
			for i, tag := range tags {
				if i > 0 {
					fmt.Fprint(out, cmd.Delimiter)
				}

				ft, err := format(tag)
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
		fmt.Fprintf(container.Stderr, "\nFound %d %s\n", count, strings.Pluralize("tag", count))
	}

	return err
}

func (cmd *TagList) tagTemplate() string {
	format := cmd.Format
	if format == "" {
		format = "full"
	}

	templ, ok := defaultTagFormats[format]
	if !ok {
		templ = strings.ExpandWhitespaceLiterals(format)
	}

	return templ
}

var defaultTagFormats = map[string]string{
	"json":  `{{json .}}`,
	"jsonl": `{{json .}}`,
	"name":  `{{name}}`,
	"full":  `{{name}} ({{note-count}})`,
}
