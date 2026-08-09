//go:build !windows

package plugintest

import (
	"os/exec"
	"syscall"
	"testing"
)

func sigterm(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Logf("SIGTERM: %v", err)
	}
}
