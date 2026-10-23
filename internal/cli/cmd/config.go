package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/zk-org/zk/internal/cli"
	"github.com/zk-org/zk/internal/cli/spec"
	"github.com/zk-org/zk/internal/util/strings"
)

// Config lists the configuration objects of a notebook.
type Config struct {
	List       string
	Format     string
	Header     string
	Footer     string
	Delimiter  string
	Delimiter0 bool
	NoPager    bool
	Quiet      bool
}

// ConfigSpec describes the `zk config` command.
func ConfigSpec(container *cli.Container) *spec.Command {
	cmd := Config{}

	return &spec.Command{
		Name:  "config",
		Short: "List configuration parameters.",
		Group: GroupNotebook,
		Flags: []spec.Flag{
			{Name: "list", Short: "l", Placeholder: "OBJECT", Target: &cmd.List,
				Help:     "List configuration objects. Listable objects are: aliases, filters and extras.",
				Complete: spec.Values("aliases", "filters", "extras")},

			{Name: "format", Short: "f", Group: spec.GroupFormat, Placeholder: "TEMPLATE", Target: &cmd.Format,
				Help:     "Pretty print the list using a custom template or predefined formats: short, full, json.",
				Complete: spec.Values("short", "full", "json")},
			{Name: "header", Group: spec.GroupFormat, Target: &cmd.Header,
				Help: "Arbitrary text printed at the start of the list."},
			{Name: "footer", Group: spec.GroupFormat, Default: `\n`, Target: &cmd.Footer,
				Help: "Arbitrary text printed at the end of the list."},
			{Name: "delimiter", Short: "d", Group: spec.GroupFormat, Default: `\n`, Target: &cmd.Delimiter,
				Help: "Print tags delimited by the given separator."},
			{Name: "delimiter0", Short: "0", Group: spec.GroupFormat, Target: &cmd.Delimiter0,
				Help: "Print tags delimited by ASCII NUL characters. Useful with xargs -0."},
			{Name: "no-pager", Short: "P", Group: spec.GroupFormat, Target: &cmd.NoPager,
				Help: "Do not pipe output into a pager."},
			{Name: "quiet", Short: "q", Group: spec.GroupFormat, Target: &cmd.Quiet,
				Help: "Do not print the total number of tags found."},
		},
		Run: func() error { return cmd.Run(container) },
	}
}

func (cmd *Config) Run(container *cli.Container) error {
	cmd.Header = strings.ExpandWhitespaceLiterals(cmd.Header)
	cmd.Footer = strings.ExpandWhitespaceLiterals(cmd.Footer)
	cmd.Delimiter = strings.ExpandWhitespaceLiterals(cmd.Delimiter)

	if cmd.Delimiter0 {
		if cmd.Header != "" {
			return errors.New("--header and --delimiter0 can't be used together")
		}
		if cmd.Footer != "\n" {
			return errors.New("--footer and --delimiter0 can't be used together")
		}
		if cmd.Delimiter != "\n" {
			return errors.New("--delimiter and --delimiter0 can't be used together")
		}

		cmd.Delimiter = "\x00"
		cmd.Footer = "\x00"
	}

	if cmd.Format == "json" {
		if cmd.Header != "" {
			return errors.New("--header can't be used with JSON format")
		}
		if cmd.Footer != "\n" {
			return errors.New("--footer can't be used with JSON format")
		}
		if cmd.Delimiter != "\n" {
			return errors.New("--delimiter can't be used with JSON format")
		}

		if cmd.Format == "json" {
			cmd.Delimiter = ","
			cmd.Header = "{"
			cmd.Footer = "}\n"
		}
	}

	var objects = make(map[string]string)

	switch cmd.List {
	case "filters":
		objects = container.Config.Filters
	case "aliases":
		objects = container.Config.Aliases
	case "extras":
		objects = container.Config.Extra
	default:
		return errors.New("listable config objects are: filters, aliases and extras")
	}

	count := len(objects)
	keys := make([]string, count)
	i := 0
	for k := range objects {
		keys[i] = k
		i++
	}
	sort.Strings(keys)

	format := cmd.mapTemplate()

	var err = container.Paginate(cmd.NoPager, func(out io.Writer) error {
		if cmd.Header != "" {
			fmt.Fprint(out, cmd.Header)
		}
		for i, o := range keys {

			if i > 0 {
				fmt.Fprint(out, cmd.Delimiter)
			}
			if cmd.Format == "" || cmd.Format == "short" {
				fmt.Fprintf(out, format, o)
			} else if cmd.Format == "json" {
				jsonData, err := json.Marshal(objects[o])
				if err != nil {
					return err
				}
				fmt.Fprintf(out, format, o, jsonData)
			} else {
				fmt.Fprintf(out, format, o, objects[o])
			}

			i += 1
		}
		if cmd.Footer != "" {
			fmt.Fprint(out, cmd.Footer)
		}
		return nil
	})

	if err == nil && !cmd.Quiet {
		fmt.Fprintf(container.Stderr, "\nFound %d %s\n", count, cmd.List)
	}
	return err
}

func (cmd *Config) mapTemplate() string {
	format := cmd.Format
	if format == "" {
		format = "short"
	}

	templ, ok := defaultMapFormats[format]
	if !ok {
		templ = strings.ExpandWhitespaceLiterals(format)
	}

	return templ
}

var defaultMapFormats = map[string]string{
	"json":  `"%s":%s`,
	"short": `%s`,
	"full":  `%12s    %s`,
}
