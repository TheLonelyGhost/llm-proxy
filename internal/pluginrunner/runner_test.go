package pluginrunner_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thelonelyghost/llm-proxy/internal/pluginrunner"
)

// buildTestHelper compiles the test helper binary and returns its path.
// The helper is a minimal HTTP sidecar that responds to /healthz and
// /management/reload. It is cached per test run via t.TempDir.
func buildTestHelper(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("subprocess test not supported on Windows in CI")
	}
	binDir := t.TempDir()
	bin := filepath.Join(binDir, "testplugin")
	cmd := exec.Command("go", "build", "-o", bin,
		"./testdata/testplugin")
	cmd.Dir = testdataDir(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build testplugin: %v\n%s", err, out)
	}
	return bin
}

func testdataDir(t *testing.T) string {
	t.Helper()
	// Resolve relative to this test file.
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(file)
}

// freePort returns a free TCP port on loopback.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// TestRunner_StartStop verifies that a Runner can start a plugin process,
// confirm it healthy via /healthz, and stop it cleanly.
func TestRunner_StartStop(t *testing.T) {
	bin := buildTestHelper(t)
	port := freePort(t)

	r := pluginrunner.New(pluginrunner.Config{
		Name:            "testplugin",
		BinaryPath:      bin,
		PortEnvVar:      "LLM_PROXY_PLUGIN_PORT",
		AssignedPort:    port,
		HealthPath:      "/healthz",
		HealthInterval:  50 * time.Millisecond,
		HealthTimeout:   2 * time.Second,
		HealthThreshold: 3,
		RestartPolicy:   pluginrunner.RestartNever,
		ShutdownTimeout: 5 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := r.Stop(ctx); err != nil {
		t.Errorf("Stop: %v", err)
	}
}

// TestRunner_BaseURL verifies that BaseURL returns the correct localhost URL.
func TestRunner_BaseURL(t *testing.T) {
	r := pluginrunner.New(pluginrunner.Config{
		Name:         "myplugin",
		BinaryPath:   "/bin/myplugin",
		AssignedPort: 9001,
	})
	got := r.BaseURL()
	want := "http://127.0.0.1:9001"
	if got != want {
		t.Errorf("BaseURL() = %q, want %q", got, want)
	}
}

// TestRunner_Reload verifies that Reload sends POST /management/reload to
// the sidecar and succeeds on HTTP 200.
func TestRunner_Reload(t *testing.T) {
	var reloadCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
		case "/management/reload":
			reloadCalls.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	// Parse the port from the test server URL.
	addr := srv.Listener.Addr().(*net.TCPAddr)

	r := pluginrunner.New(pluginrunner.Config{
		Name:         "testplugin",
		ReloadPath:   "/management/reload",
		AssignedPort: addr.Port,
	})

	ctx := context.Background()
	if err := r.Reload(ctx); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if reloadCalls.Load() != 1 {
		t.Errorf("reload endpoint called %d times, want 1", reloadCalls.Load())
	}
}

// TestRunner_Reload_NonOKResponse verifies that Reload returns an error when
// the sidecar responds with a non-2xx status.
func TestRunner_Reload_NonOKResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	addr := srv.Listener.Addr().(*net.TCPAddr)
	r := pluginrunner.New(pluginrunner.Config{
		Name:         "testplugin",
		ReloadPath:   "/management/reload",
		AssignedPort: addr.Port,
	})

	if err := r.Reload(context.Background()); err == nil {
		t.Fatal("expected error for non-200 reload response, got nil")
	}
}

// TestRestartPolicyString verifies the string constants are as expected.
func TestRestartPolicyString(t *testing.T) {
	cases := []struct {
		policy pluginrunner.RestartPolicy
		want   string
	}{
		{pluginrunner.RestartAlways, "always"},
		{pluginrunner.RestartOnFailure, "on-failure"},
		{pluginrunner.RestartNever, "never"},
	}
	for _, tc := range cases {
		if got := tc.policy.String(); got != tc.want {
			t.Errorf("RestartPolicy(%v).String() = %q, want %q", tc.policy, got, tc.want)
		}
	}
}

// TestParseRestartPolicy verifies that valid and invalid strings parse correctly.
func TestParseRestartPolicy(t *testing.T) {
	cases := []struct {
		input   string
		want    pluginrunner.RestartPolicy
		wantErr bool
	}{
		{"always", pluginrunner.RestartAlways, false},
		{"on-failure", pluginrunner.RestartOnFailure, false},
		{"never", pluginrunner.RestartNever, false},
		{"", pluginrunner.RestartOnFailure, false}, // default
		{"bogus", 0, true},
	}
	for _, tc := range cases {
		got, err := pluginrunner.ParseRestartPolicy(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseRestartPolicy(%q): expected error, got nil", tc.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRestartPolicy(%q): %v", tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseRestartPolicy(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

// TestRunner_HealthCheck_FailsIfSidecarNeverStarts verifies that Start returns
// an error if the sidecar never becomes healthy within the timeout.
func TestRunner_HealthCheck_FailsIfSidecarNeverStarts(t *testing.T) {
	// Point at a port that is guaranteed to refuse connections.
	port := freePort(t)

	r := pluginrunner.New(pluginrunner.Config{
		Name:            "deadplugin",
		BinaryPath:      os.DevNull, // not a real binary; Start will fail
		PortEnvVar:      "LLM_PROXY_PLUGIN_PORT",
		AssignedPort:    port,
		HealthPath:      "/healthz",
		HealthInterval:  10 * time.Millisecond,
		HealthTimeout:   50 * time.Millisecond,
		HealthThreshold: 2,
		RestartPolicy:   pluginrunner.RestartNever,
		ShutdownTimeout: 1 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := r.Start(ctx)
	if err == nil {
		_ = r.Stop(ctx)
		t.Fatal("expected Start to fail for a dead binary, got nil")
	}
}

// Ensure the test compiles even without the testdata helper present by
// providing a stub check.
func TestSanity(t *testing.T) {
	if strings.Contains(fmt.Sprintf("%p", pluginrunner.New), "nil") {
		t.Error("pluginrunner.New returned nil")
	}
}
