//go:build windows

package cli

import (
	"context"
	"os"
	"os/exec"
)

// execPluginProcess runs the plugin binary as a child process on Windows,
// passing through stdin/stdout/stderr.
func execPluginProcess(_ context.Context, binPath string, args []string, extraEnv []string) error {
	cmd := exec.Command(binPath, args...) //nolint:gosec // operator-controlled binary, path validated by caller
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), extraEnv...)
	return cmd.Run()
}
