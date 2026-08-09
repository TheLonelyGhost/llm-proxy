//go:build windows

package plugintest

import (
	"os"
	"os/exec"
	"testing"
)

func sigterm(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if err := cmd.Process.Signal(os.Kill); err != nil {
		t.Logf("Kill: %v", err)
	}
}
