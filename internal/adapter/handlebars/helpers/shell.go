package helpers

import (
	"strings"

	"github.com/aymerick/raymond"
	"github.com/zk-org/zk/internal/util"
	"github.com/zk-org/zk/internal/util/exec"
)

// NewShellHelper returns a {{sh}} template helper function that runs shell commands.
// The shellProvider function is called to determine which shell to use.
//
// {{#sh "tr '[a-z]' '[A-Z]'"}}Hello, world!{{/sh}} -> HELLO, WORLD!
// {{sh "echo 'Hello, world!'"}} -> Hello, world!
func NewShellHelper(logger util.Logger, shell string) interface{} {
	return func(arg string, options *raymond.Options) string {
		cmd := exec.CommandFromString(shell, arg)

		// Feed any block content as piped input
		cmd.Stdin = strings.NewReader(options.Fn())

		output, err := cmd.Output()
		if err != nil {
			logger.Printf("{{sh}} command failed: %v", err)
			return ""
		}

		return strings.TrimSpace(string(output))
	}
}
