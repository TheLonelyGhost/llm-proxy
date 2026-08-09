//go:build !windows

package cli

import (
	"context"
	"os"
	"os/exec"
)

// execPluginProcess replaces the current process with the plugin binary on
// Unix via exec.Command + stdin/stdout/stderr pass-through. On Unix we could
// use syscall.Exec for a true exec, but os/exec is simpler and sufficient.
func execPluginProcess(_ context.Context, binPath string, args []string, extraEnv []string) error {
	cmd := exec.Command(binPath, args...) //nolint:gosec // operator-controlled binary, path validated by caller
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), extraEnv...)
	return cmd.Run()
}
