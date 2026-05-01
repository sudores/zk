//go:build !windows

package exec

import (
	"os/exec"

	"github.com/zk-org/zk/internal/util/opt"
	osutil "github.com/zk-org/zk/internal/util/os"
)

// ResolveShell returns the shell to use for running commands, checking in order:
// ZK_SHELL environment variable, config/tool.shell, SHELL environment variable, or "sh" as fallback.
func ResolveShell(configShell opt.String) string {
	return osutil.GetOptEnv("ZK_SHELL").
		Or(configShell).
		Or(osutil.GetOptEnv("SHELL")).
		OrString("sh").
		Unwrap()
}

// CommandFromString returns a Cmd running the given command with the specified shell.
func CommandFromString(shell, command string, args ...string) *exec.Cmd {
	args = append([]string{"-c", command, "--"}, args...)
	return exec.Command(shell, args...)
}
