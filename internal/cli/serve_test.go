package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thelonelyghost/llm-proxy/internal/cli"
)

// installServePlugin writes a fake shell-script plugin named pluginName with
// scriptBody, installs it into a temp cache, and returns the config path. The
// config declares one backend "work" of the plugin type whose body is
// backendBody.
func installServePlugin(t *testing.T, pluginName, scriptBody, backendBody string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script plugin test not supported on Windows")
	}

	fakeDir := t.TempDir()
	binName := "llm-proxy-provider-" + pluginName
	if err := os.WriteFile(filepath.Join(fakeDir, binName), []byte(scriptBody), 0o755); err != nil {
		t.Fatal(err)
	}
	manifestJSON := `{"name":"` + pluginName + `","version":"1.0.0","protocol_version":1}`
	if err := os.WriteFile(filepath.Join(fakeDir, binName+".manifest.json"), []byte(manifestJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LLM_PROXY_CACHE_DIR", t.TempDir())

	cfg := writeConfig(t, `
plugin "`+pluginName+`" {
  source  = "file://`+fakeDir+`"
  version = "1.0.0"
}
backend "work" {
  type = "`+pluginName+`"
`+backendBody+`
}
`)
	if _, err := run(t, "plugin", "install", "--config", cfg); err != nil {
		t.Fatalf("plugin install: %v", err)
	}
	return cfg
}

// TestServe_SidecarReceivesBackendEnv verifies that `serve` launches a plugin
// sidecar with LLM_PROXY_BACKEND_LABEL, LLM_PROXY_BACKEND_TYPE and
// LLM_PROXY_BACKEND_CONFIG set from the backend block.
func TestServe_SidecarReceivesBackendEnv(t *testing.T) {
	outDir := t.TempDir()
	outFile := filepath.Join(outDir, "env")
	// The fake sidecar records the three variables, one per line, then exits.
	// It never serves /healthz, so serve blocks in the readiness wait until
	// the test cancels it.
	script := "#!/bin/sh\n" +
		`printf '%s\n%s\n%s\n' "$LLM_PROXY_BACKEND_LABEL" "$LLM_PROXY_BACKEND_TYPE" "$LLM_PROXY_BACKEND_CONFIG" > '` + outFile + `.tmp'` + "\n" +
		`mv '` + outFile + `.tmp' '` + outFile + `'` + "\n"
	cfg := installServePlugin(t, "sidecarenv", script, `
  models = ["m1", "m2"]
  effort = "high"
  tags   = ["a", "b"]
`)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var buf bytes.Buffer
	root := cli.New(&buf, &buf)
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"serve", "--config", cfg, "--port", "0"})
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()

	data := waitForFile(t, outFile, done)
	cancel()
	<-done

	lines := strings.SplitN(strings.TrimSuffix(string(data), "\n"), "\n", 3)
	if len(lines) != 3 {
		t.Fatalf("sidecar env dump has %d lines, want 3: %q", len(lines), data)
	}
	if lines[0] != "work" {
		t.Errorf("LLM_PROXY_BACKEND_LABEL = %q, want %q", lines[0], "work")
	}
	if lines[1] != "sidecarenv" {
		t.Errorf("LLM_PROXY_BACKEND_TYPE = %q, want %q", lines[1], "sidecarenv")
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(lines[2]), &got); err != nil {
		t.Fatalf("LLM_PROXY_BACKEND_CONFIG is not valid JSON: %v\n%s", err, lines[2])
	}
	want := map[string]any{
		"name":   "work",
		"type":   "sidecarenv",
		"models": []any{"m1", "m2"},
		"effort": "high",
		"tags":   []any{"a", "b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LLM_PROXY_BACKEND_CONFIG = %#v, want %#v", got, want)
	}
}

// TestServe_SidecarBackendConfigError verifies that `serve` fails backend
// startup when the backend block cannot be encoded for the sidecar.
func TestServe_SidecarBackendConfigError(t *testing.T) {
	// A nested block is accepted by the loader but has no attribute form, so
	// BackendConfigJSON rejects it.
	cfg := installServePlugin(t, "sidecarbad", "#!/bin/sh\nexit 0\n", `
  nested {
    key = "value"
  }
`)
	_, err := run(t, "serve", "--config", cfg)
	if err == nil {
		t.Fatal("expected error for unencodable backend config, got nil")
	}
	if !strings.Contains(err.Error(), "backend config") {
		t.Errorf("expected backend config error, got: %v", err)
	}
}

// waitForFile polls for path until it exists or a deadline passes. It fails
// the test early if serve returns before the file appears.
func waitForFile(t *testing.T, path string, done <-chan error) []byte {
	t.Helper()
	deadline := time.After(20 * time.Second)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if data, err := os.ReadFile(path); err == nil {
			return data
		}
		select {
		case err := <-done:
			t.Fatalf("serve returned before sidecar started: %v", err)
		case <-deadline:
			t.Fatalf("timed out waiting for %s", path)
		case <-tick.C:
		}
	}
}
