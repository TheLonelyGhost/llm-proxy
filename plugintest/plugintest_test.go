package plugintest_test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/thelonelyghost/llm-proxy/plugintest"
)

// buildTestPlugin compiles the minimal test plugin binary for conformance testing.
func buildTestPlugin(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("subprocess conformance test not supported on Windows in CI")
	}
	binDir := t.TempDir()
	bin := filepath.Join(binDir, "conformant-plugin")
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	srcDir := filepath.Join(filepath.Dir(file), "testdata", "conformant")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = srcDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build conformant plugin: %v\n%s", err, out)
	}
	return bin
}

// TestRun_ConformantPlugin verifies that a fully conformant plugin passes all
// plugintest checks.
func TestRun_ConformantPlugin(t *testing.T) {
	bin := buildTestPlugin(t)
	plugintest.Run(t, plugintest.Options{
		Binary: bin,
	})
}
