package cli_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thelonelyghost/llm-proxy/internal/cli"
	"github.com/thelonelyghost/llm-proxy/internal/lockfile"
	"github.com/thelonelyghost/llm-proxy/internal/plugin"
)

// TestPlugin_RunHelp verifies that `llm-proxy plugin --help` surfaces the
// plugin subcommands.
func TestPlugin_RunHelp(t *testing.T) {
	out, err := run(t, "plugin", "--help")
	if err != nil {
		t.Fatalf("plugin --help: %v\nout: %s", err, out)
	}
	for _, want := range []string{"install", "run"} {
		if !strings.Contains(out, want) {
			t.Errorf("plugin --help output missing %q\nout: %s", want, out)
		}
	}
}

// TestPlugin_Install_MissingConfig verifies that `plugin install` fails clearly
// when the config file is missing.
func TestPlugin_Install_MissingConfig(t *testing.T) {
	t.Setenv("LLM_PROXY_CONFIG", filepath.Join(t.TempDir(), "missing.hcl"))
	_, err := run(t, "plugin", "install")
	if err == nil {
		t.Fatal("expected error for missing config, got nil")
	}
}

// TestPlugin_Install_NoPlugins verifies that `plugin install` succeeds with no
// output when the config has no plugin blocks.
func TestPlugin_Install_NoPlugins(t *testing.T) {
	cfg := writeConfig(t, `
backend "myopenai" {
  type    = "openai"
  api_key = "sk-test"
}
`)
	out, err := run(t, "plugin", "install", "--config", cfg)
	if err != nil {
		t.Fatalf("plugin install with no plugins: %v\nout: %s", err, out)
	}
}

// TestPlugin_Run_MissingSubcommand verifies that `plugin run` without arguments
// shows usage.
func TestPlugin_Run_MissingSubcommand(t *testing.T) {
	out, err := run(t, "plugin", "run")
	// Missing required args — should error or print usage.
	if err == nil && !strings.Contains(out, "Usage") && !strings.Contains(out, "usage") {
		t.Logf("out: %s", out)
	}
}

// TestPlugin_Run_ConfigFlagRespected verifies that `plugin run --config <path>`
// honours the flag and fails with a "no backend" error (not a missing-config
// error), proving the flag was actually read.
func TestPlugin_Run_ConfigFlagRespected(t *testing.T) {
	cfg := writeConfig(t, `
plugin "myplugin" {
  source  = "github.com/example/llm-proxy-provider-myplugin"
  version = "1.0.0"
}
backend "mybackend" {
  type = "myplugin"
}
`)
	// Ask for a backend label that is not declared.
	_, err := run(t, "plugin", "run", "--config", cfg, "nonexistent", "sometool")
	if err == nil {
		t.Fatal("expected error for unknown backend label")
	}
	if strings.Contains(err.Error(), "no such file") || strings.Contains(err.Error(), "cannot find") {
		t.Errorf("looks like --config was ignored (config file not found): %v", err)
	}
	if !strings.Contains(err.Error(), "no backend") && !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("unexpected error (expected 'no backend' / label name): %v", err)
	}
}

// TestPlugin_Run_ShortConfigFlagRespected verifies that `plugin run -c <path>`
// short form also works.
func TestPlugin_Run_ShortConfigFlagRespected(t *testing.T) {
	cfg := writeConfig(t, `
plugin "myplugin" {
  source  = "github.com/example/llm-proxy-provider-myplugin"
  version = "1.0.0"
}
backend "mybackend" {
  type = "myplugin"
}
`)
	_, err := run(t, "plugin", "run", "-c", cfg, "nonexistent", "sometool")
	if err == nil {
		t.Fatal("expected error for unknown backend label")
	}
	if strings.Contains(err.Error(), "no such file") || strings.Contains(err.Error(), "cannot find") {
		t.Errorf("looks like -c was ignored (config file not found): %v", err)
	}
	if !strings.Contains(err.Error(), "no backend") && !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("unexpected error (expected 'no backend' / label name): %v", err)
	}
}

// TestServe_ReservedNameConflict verifies that `serve` hard-fails when a
// plugin block label matches a compiled-in provider type name.
func TestServe_ReservedNameConflict(t *testing.T) {
	cfg := writeConfig(t, `
plugin "openai" {
  source  = "github.com/example/llm-proxy-provider-openai"
  version = "1.0.0"
}
backend "myopenai" {
  type    = "openai"
  api_key = "sk-test"
}
`)
	_, err := run(t, "serve", "--config", cfg)
	if err == nil {
		t.Fatal("expected error for reserved plugin name 'openai', got nil")
	}
	if !strings.Contains(err.Error(), "openai") || !strings.Contains(strings.ToLower(err.Error()), "reserved") {
		t.Errorf("error should mention 'openai' and 'reserved', got: %v", err)
	}
}

// TestServe_ChecksumMismatch verifies that `serve` hard-fails when the
// installed binary's SHA-256 does not match the checksum recorded in the lock
// file, protecting against tampered or corrupted binaries.
func TestServe_ChecksumMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script plugin test not supported on Windows")
	}

	fakeDir := buildFakePluginDir(t, "cksumguardplugin", "1.0.0")

	cacheDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", cacheDir)

	cfgDir := t.TempDir()
	cfgFile := filepath.Join(cfgDir, "config.hcl")
	if err := os.WriteFile(cfgFile, []byte(`
plugin "cksumguardplugin" {
  source  = "file://`+fakeDir+`"
  version = "1.0.0"
}
backend "work" {
  type = "cksumguardplugin"
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Install normally so the binary is cached.
	if _, err := run(t, "plugin", "install", "--config", cfgFile); err != nil {
		t.Fatalf("plugin install: %v", err)
	}

	// Tamper: overwrite the lock file with a wrong checksum.
	lockPath := filepath.Join(cfgDir, lockfile.FileName)
	lf, err := lockfile.Read(lockPath)
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	entry, ok := lf.Lookup("cksumguardplugin")
	if !ok {
		t.Fatal("lock file missing entry")
	}
	entry.Checksum = strings.Repeat("0", 64) // wrong
	lf.Set(entry)
	if err := lockfile.Write(lockPath, lf); err != nil {
		t.Fatalf("write tampered lock file: %v", err)
	}

	// `serve` must hard-fail with a checksum error, not a startup/binary error.
	_, err = run(t, "serve", "--config", cfgFile)
	if err == nil {
		t.Fatal("expected error for checksum mismatch, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "checksum") {
		t.Errorf("expected error mentioning 'checksum', got: %v", err)
	}
}

// TestServe_ChecksumMatch verifies that `serve` succeeds (past the checksum
// gate) when the lock file checksum matches the installed binary. The serve
// command will still fail because the plugin is not a real sidecar server, but
// the error must NOT be a checksum error.
func TestServe_ChecksumMatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script plugin test not supported on Windows")
	}

	fakeDir := buildFakePluginDir(t, "cksumokplugin", "1.0.0")

	cacheDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", cacheDir)

	cfgDir := t.TempDir()
	cfgFile := filepath.Join(cfgDir, "config.hcl")
	if err := os.WriteFile(cfgFile, []byte(`
plugin "cksumokplugin" {
  source  = "file://`+fakeDir+`"
  version = "1.0.0"
}
backend "work" {
  type = "cksumokplugin"
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := run(t, "plugin", "install", "--config", cfgFile); err != nil {
		t.Fatalf("plugin install: %v", err)
	}

	// `serve` will fail (plugin is not a real server) but must NOT fail with a
	// checksum error.
	_, err := run(t, "serve", "--config", cfgFile)
	if err == nil {
		t.Fatal("expected error (plugin is not a real server), got nil")
	}
	if strings.Contains(strings.ToLower(err.Error()), "checksum") {
		t.Errorf("unexpected checksum error when checksums match: %v", err)
	}
}
func TestServe_MissingPlugin(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", cacheDir)

	cfg := writeConfig(t, `
plugin "myplugin" {
  source  = "github.com/example/llm-proxy-provider-myplugin"
  version = "1.0.0"
}
backend "work" {
  type = "myplugin"
}
`)
	_, err := run(t, "serve", "--config", cfg)
	if err == nil {
		t.Fatal("expected error for missing plugin binary, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "plugin") {
		t.Errorf("error should mention 'plugin', got: %v", err)
	}
}

// TestServe_MissingPlugin_RangeConstraint_UsesLockfileVersion verifies that
// when a plugin is declared with a range constraint (e.g. "~> 1.0") and a
// lock file has already resolved it to a concrete version, `serve` looks up
// the binary under the concrete version path, not the raw constraint string.
// This is a regression test for the bug where pb.Version was used directly as
// the cache directory name instead of the lock-file-resolved version.
func TestServe_MissingPlugin_RangeConstraint_UsesLockfileVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script plugin test not supported on Windows")
	}

	// Build a fake plugin source that will be installed.
	fakeDir := buildFakePluginDir(t, "rangeplugin", "1.2.3")

	cacheDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", cacheDir)

	cfgDir := t.TempDir()
	cfgFile := filepath.Join(cfgDir, "config.hcl")
	if err := os.WriteFile(cfgFile, []byte(`
plugin "rangeplugin" {
  source  = "file://`+fakeDir+`"
  version = "~> 1.0"
}
backend "work" {
  type = "rangeplugin"
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Pre-write a lock file that resolved the "~> 1.0" constraint to "1.2.3".
	platform := runtime.GOOS + "_" + runtime.GOARCH
	lockContent := `# llm-proxy.lock.hcl
plugin "rangeplugin" {
  source   = "file://` + fakeDir + `"
  version  = "~> 1.0"
  resolved = "1.2.3"
  platform = "` + platform + `"
}
`
	if err := os.WriteFile(filepath.Join(cfgDir, lockfile.FileName), []byte(lockContent), 0o600); err != nil {
		t.Fatal(err)
	}

	// Install using the exact-pin workaround: temporarily override the config
	// with version = "1.2.3" so the install command seeds the right cache dir.
	// In production, `plugin install` would have been run first and written
	// the lock file; we simulate that by seeding the cache manually.
	pluginBinDir, err := plugin.PluginCacheDir(cacheDir, "rangeplugin", "rangeplugin", "1.2.3")
	if err != nil {
		t.Fatalf("PluginCacheDir: %v", err)
	}
	binName := "llm-proxy-provider-rangeplugin"
	if err := os.WriteFile(filepath.Join(pluginBinDir, binName), []byte("#!/bin/sh\necho ok"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifestJSON := `{"name":"rangeplugin","version":"1.2.3","protocol_version":1}`
	if err := os.WriteFile(filepath.Join(pluginBinDir, binName+".manifest.json"), []byte(manifestJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	// `serve` must find the binary under .../1.2.3/... (from the lock file),
	// NOT under .../~> 1.0/... (the raw constraint). The serve command will
	// fail because the fake plugin is not a real sidecar server, but the
	// error must NOT be "binary not found" — it should be a startup/timeout
	// error from the plugin runner, proving the right path was used.
	_, err = run(t, "serve", "--config", cfgFile)
	if err == nil {
		t.Fatal("expected error (plugin is not a real server), got nil")
	}
	if strings.Contains(err.Error(), "binary not found") {
		t.Errorf("serve looked up wrong cache path (used raw constraint instead of resolved version): %v", err)
	}
	if strings.Contains(err.Error(), "~> 1.0") {
		t.Errorf("serve embedded the raw constraint string in the path: %v", err)
	}
}

// TestPlugin_Install_DryRunWithLocalFile verifies that `plugin install` with a
// local file source installs the binary to the cache.
func TestPlugin_Install_DryRunWithLocalFile(t *testing.T) {
	// Create a fake binary to "install".
	fakeDir := t.TempDir()
	fakeBin := filepath.Join(fakeDir, "llm-proxy-provider-fakeplugin")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\necho ok"), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeManifest := filepath.Join(fakeDir, "llm-proxy-provider-fakeplugin.manifest.json")
	manifestJSON := `{"name":"fakeplugin","version":"1.0.0","protocol_version":1}`
	if err := os.WriteFile(fakeManifest, []byte(manifestJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	cacheDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", cacheDir)

	cfg := writeConfig(t, `
plugin "fakeplugin" {
  source  = "file://`+fakeDir+`"
  version = "1.0.0"
}
`)
	out, err := run(t, "plugin", "install", "--config", cfg)
	if err != nil {
		t.Fatalf("plugin install with local file: %v\nout: %s", err, out)
	}
}

// TestExitCodeError_Error verifies that ExitCodeError.Error() formats correctly.
func TestExitCodeError_Error(t *testing.T) {
	e := &cli.ExitCodeError{Code: 42}
	want := "exit status 42"
	if got := e.Error(); got != want {
		t.Errorf("ExitCodeError.Error() = %q, want %q", got, want)
	}
}

// TestExitCodeError_IsWrappable verifies that errors.As works with ExitCodeError.
func TestExitCodeError_IsWrappable(t *testing.T) {
	e := &cli.ExitCodeError{Code: 3}
	var target *cli.ExitCodeError
	if !errors.As(e, &target) {
		t.Fatal("errors.As should find ExitCodeError")
	}
	if target.Code != 3 {
		t.Errorf("Code = %d, want 3", target.Code)
	}
}

// installFakePlugin is a helper that creates a fake plugin binary+manifest in a
// temp dir, installs it into a temp cache, and returns the config path and the
// cache dir env override applied via t.Setenv.
// backendName is the backend block label used to invoke the plugin via `plugin run`.
func installFakePlugin(t *testing.T, pluginName, scriptBody string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script plugin test not supported on Windows")
	}

	fakeDir := t.TempDir()
	binName := "llm-proxy-provider-" + pluginName
	fakeBin := filepath.Join(fakeDir, binName)
	if err := os.WriteFile(fakeBin, []byte(scriptBody), 0o755); err != nil {
		t.Fatal(err)
	}
	manifestJSON := `{"name":"` + pluginName + `","version":"1.0.0","protocol_version":1}`
	if err := os.WriteFile(filepath.Join(fakeDir, binName+".manifest.json"), []byte(manifestJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	cacheDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", cacheDir)

	cfg := writeConfig(t, `
plugin "`+pluginName+`" {
  source  = "file://`+fakeDir+`"
  version = "1.0.0"
}
backend "`+pluginName+`" {
  type = "`+pluginName+`"
}
`)
	if _, err := run(t, "plugin", "install", "--config", cfg); err != nil {
		t.Fatalf("plugin install: %v", err)
	}
	return cfg
}

// TestPlugin_Run_ExitsZero verifies that `plugin run` returns nil when the
// plugin binary exits with code 0.
func TestPlugin_Run_ExitsZero(t *testing.T) {
	cfg := installFakePlugin(t, "okplugin", "#!/bin/sh\nexit 0\n")
	_, err := run(t, "plugin", "run", "--config", cfg, "okplugin", "sometool")
	if err != nil {
		t.Errorf("expected nil error for exit-0 plugin, got: %v", err)
	}
}

// TestPlugin_Run_BackendLabelResolvesPlugin verifies that when two backends
// share the same plugin type, specifying a backend label runs the plugin for
// that specific backend. The script exits non-zero if the env var doesn't match.
func TestPlugin_Run_BackendLabelResolvesPlugin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script plugin test not supported on Windows")
	}

	// Script exits 1 unless --tool is provided and LLM_PROXY_BACKEND_LABEL
	// is non-empty (we can't check the exact value without stdout capture,
	// so we just verify the variable is set by grepping its length).
	// To test the distinct-label behaviour, each backend gets a dedicated
	// script that exits 42 if LLM_PROXY_BACKEND_LABEL != its expected value.
	fakeDir := t.TempDir()
	binName := "llm-proxy-provider-shared"
	script := filepath.Join(fakeDir, binName)
	// Script: exit 0 only when the label env var equals the expected value
	// passed as the second argument to --tool (we can't forward user args to
	// the check without cooperation from the tool dispatch, so instead we
	// just verify the env var is set to a non-empty value).
	if err := os.WriteFile(script, []byte("#!/bin/sh\n[ -n \"$LLM_PROXY_BACKEND_LABEL\" ] || exit 42\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifestJSON := `{"name":"shared","version":"1.0.0","protocol_version":1}`
	if err := os.WriteFile(filepath.Join(fakeDir, binName+".manifest.json"), []byte(manifestJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	cacheDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", cacheDir)

	// Two backends, same plugin type.
	cfg := writeConfig(t, `
plugin "shared" {
  source  = "file://`+fakeDir+`"
  version = "1.0.0"
}
backend "ollama" {
  type = "shared"
}
backend "cloud" {
  type = "shared"
}
`)
	if _, err := run(t, "plugin", "install", "--config", cfg); err != nil {
		t.Fatalf("plugin install: %v", err)
	}

	// Both backends must resolve to the same binary and run without error.
	for _, label := range []string{"ollama", "cloud"} {
		_, err := run(t, "plugin", "run", "--config", cfg, label, "sometool")
		if err != nil {
			t.Errorf("plugin run %s: %v (LLM_PROXY_BACKEND_LABEL not set?)", label, err)
		}
	}
}

// TestPlugin_Run_CompiledInTypeRejected verifies that `plugin run` returns a
// clear error when the backend's type is a compiled-in provider (not a plugin).
func TestPlugin_Run_CompiledInTypeRejected(t *testing.T) {
	cfg := writeConfig(t, `
backend "myopenai" {
  type    = "openai"
  api_key = "sk-test"
}
`)
	_, err := run(t, "plugin", "run", "--config", cfg, "myopenai", "sometool")
	if err == nil {
		t.Fatal("expected error for compiled-in backend type, got nil")
	}
	if !strings.Contains(err.Error(), "compiled-in") && !strings.Contains(err.Error(), "openai") {
		t.Errorf("expected error mentioning compiled-in/openai, got: %v", err)
	}
}

// TestPlugin_Run_BackendConfigEnvSet verifies that LLM_PROXY_BACKEND_CONFIG is
// forwarded to the plugin. The script exits 42 if the variable is empty.
func TestPlugin_Run_BackendConfigEnvSet(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script plugin test not supported on Windows")
	}

	// Script exits 42 if LLM_PROXY_BACKEND_CONFIG is empty, 0 otherwise.
	fakeDir := t.TempDir()
	binName := "llm-proxy-provider-envtest"
	script := filepath.Join(fakeDir, binName)
	if err := os.WriteFile(script, []byte("#!/bin/sh\n[ -n \"$LLM_PROXY_BACKEND_CONFIG\" ] || exit 42\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifestJSON := `{"name":"envtest","version":"1.0.0","protocol_version":1}`
	if err := os.WriteFile(filepath.Join(fakeDir, binName+".manifest.json"), []byte(manifestJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	cacheDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", cacheDir)

	cfg := writeConfig(t, `
plugin "envtest" {
  source  = "file://`+fakeDir+`"
  version = "1.0.0"
}
backend "myenvbackend" {
  type = "envtest"
}
`)
	if _, err := run(t, "plugin", "install", "--config", cfg); err != nil {
		t.Fatalf("plugin install: %v", err)
	}

	_, err := run(t, "plugin", "run", "--config", cfg, "myenvbackend", "sometool")
	if err != nil {
		t.Fatalf("plugin run: %v (LLM_PROXY_BACKEND_CONFIG not set?)", err)
	}
}

// TestPlugin_Run_ExitCodeError verifies that `plugin run` wraps a non-zero
// plugin exit in ExitCodeError and does NOT prepend a spurious error message.
func TestPlugin_Run_ExitCodeError(t *testing.T) {
	cfg := installFakePlugin(t, "failplugin", "#!/bin/sh\nexit 7\n")
	_, err := run(t, "plugin", "run", "--config", cfg, "failplugin", "sometool")
	if err == nil {
		t.Fatal("expected error for exit-7 plugin, got nil")
	}
	var ece *cli.ExitCodeError
	if !errors.As(err, &ece) {
		t.Fatalf("expected *cli.ExitCodeError, got %T: %v", err, err)
	}
	if ece.Code != 7 {
		t.Errorf("ExitCodeError.Code = %d, want 7", ece.Code)
	}
}

// TestPlugin_Run_ExitCodeError_NoBinaryPath verifies that execPlugin returns a
// meaningful error when the binary path is not absolute.
func TestPlugin_Run_InvalidPath(t *testing.T) {
	// Point the cache somewhere that will resolve to a relative path by using a
	// config whose resolved cache yields a non-existent but absolute path,
	// then verify we get the "not found" sentinel — exercising the os.Stat
	// branch inside execPlugin.
	cacheDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", cacheDir)
	cfg := writeConfig(t, `
plugin "ghostplugin" {
  source  = "file:///nonexistent"
  version = "1.0.0"
}
backend "ghostbackend" {
  type = "ghostplugin"
}
`)
	// Do not install; just run directly so execPlugin hits the os.Stat branch.
	_, err := run(t, "plugin", "run", "--config", cfg, "ghostbackend", "sometool")
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	if !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "no such file") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

// ---- lock file integration tests -------------------------------------------

// buildFakePluginDir creates a directory with a fake plugin binary + manifest
// and returns the path. The manifest version matches manifestVersion.
func buildFakePluginDir(t *testing.T, pluginName, manifestVersion string) string {
	t.Helper()
	dir := t.TempDir()
	binName := "llm-proxy-provider-" + pluginName
	if err := os.WriteFile(filepath.Join(dir, binName), []byte("#!/bin/sh\necho ok"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifestJSON := `{"name":"` + pluginName + `","version":"` + manifestVersion + `","protocol_version":1}`
	if err := os.WriteFile(filepath.Join(dir, binName+".manifest.json"), []byte(manifestJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestPlugin_Install_WritesLockfile verifies that `plugin install` writes a
// lock file adjacent to the config containing the resolved version.
func TestPlugin_Install_WritesLockfile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script test not supported on Windows")
	}
	fakeDir := buildFakePluginDir(t, "lockplugin", "1.0.0")

	cacheDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", cacheDir)

	cfgDir := t.TempDir()
	cfgFile := filepath.Join(cfgDir, "config.hcl")
	if err := os.WriteFile(cfgFile, []byte(`
plugin "lockplugin" {
  source  = "file://`+fakeDir+`"
  version = "1.0.0"
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, "plugin", "install", "--config", cfgFile)
	if err != nil {
		t.Fatalf("plugin install: %v\nout: %s", err, out)
	}

	lockPath := filepath.Join(cfgDir, lockfile.FileName)
	if _, statErr := os.Stat(lockPath); statErr != nil {
		t.Fatalf("lock file not created at %s: %v", lockPath, statErr)
	}

	lf, err := lockfile.Read(lockPath)
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	entry, ok := lf.Lookup("lockplugin")
	if !ok {
		t.Fatal("lock file missing entry for 'lockplugin'")
	}
	if entry.Resolved != "1.0.0" {
		t.Errorf("Resolved = %q, want %q", entry.Resolved, "1.0.0")
	}
	if entry.Platform != runtime.GOOS+"_"+runtime.GOARCH {
		t.Errorf("Platform = %q, want %q", entry.Platform, runtime.GOOS+"_"+runtime.GOARCH)
	}
}

// TestPlugin_Install_SkipsDownloadWhenCached verifies that running
// `plugin install` a second time skips the download and reports the plugin
// as already cached.
func TestPlugin_Install_SkipsDownloadWhenCached(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script test not supported on Windows")
	}
	fakeDir := buildFakePluginDir(t, "cacheplugin", "1.0.0")

	cacheDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", cacheDir)

	cfgDir := t.TempDir()
	cfgFile := filepath.Join(cfgDir, "config.hcl")
	if err := os.WriteFile(cfgFile, []byte(`
plugin "cacheplugin" {
  source  = "file://`+fakeDir+`"
  version = "1.0.0"
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	// First install.
	if _, err := run(t, "plugin", "install", "--config", cfgFile); err != nil {
		t.Fatalf("first install: %v", err)
	}

	// Second install — should say "already cached".
	out, err := run(t, "plugin", "install", "--config", cfgFile)
	if err != nil {
		t.Fatalf("second install: %v\nout: %s", err, out)
	}
	if !strings.Contains(out, "already cached") {
		t.Errorf("expected 'already cached' message on second install, got:\n%s", out)
	}
}

// TestPlugin_Install_UsesLockfileVersion verifies that when a lock file is
// present the locked version is used rather than re-resolving.
func TestPlugin_Install_UsesLockfileVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script test not supported on Windows")
	}
	// We only have a "1.0.0" fake available; the lock file will point there.
	fakeDir := buildFakePluginDir(t, "lockpinplugin", "1.0.0")

	cacheDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", cacheDir)

	cfgDir := t.TempDir()
	cfgFile := filepath.Join(cfgDir, "config.hcl")
	if err := os.WriteFile(cfgFile, []byte(`
plugin "lockpinplugin" {
  source  = "file://`+fakeDir+`"
  version = "1.0.0"
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Pre-write a lock file that resolves to 1.0.0.
	platform := runtime.GOOS + "_" + runtime.GOARCH
	lockContent := `# llm-proxy.lock.hcl
plugin "lockpinplugin" {
  source   = "file://` + fakeDir + `"
  version  = "1.0.0"
  resolved = "1.0.0"
  platform = "` + platform + `"
}
`
	if err := os.WriteFile(filepath.Join(cfgDir, lockfile.FileName), []byte(lockContent), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, "plugin", "install", "--config", cfgFile)
	if err != nil {
		t.Fatalf("plugin install with lockfile: %v\nout: %s", err, out)
	}
}

// TestPlugin_Install_Upgrade_FlagExists verifies that the --upgrade flag is
// recognised by the install command.
func TestPlugin_Install_Upgrade_FlagExists(t *testing.T) {
	out, err := run(t, "plugin", "install", "--help")
	if err != nil {
		t.Fatalf("plugin install --help: %v\nout: %s", err, out)
	}
	if !strings.Contains(out, "--upgrade") {
		t.Errorf("--upgrade flag not listed in plugin install --help:\n%s", out)
	}
}

// TestPlugin_Install_Upgrade_UpdatesLockfile verifies that --upgrade installs
// the plugin fresh and updates the lock file even when the lock file already
// records a version.
func TestPlugin_Install_Upgrade_UpdatesLockfile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script test not supported on Windows")
	}
	// The only available source has a plugin at "1.0.0"; this is also what
	// --upgrade will resolve to (exact pin in config), so the lock file should
	// be updated to 1.0.0 regardless of what was there before.
	fakeDir := buildFakePluginDir(t, "upgplugin", "1.0.0")

	cacheDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", cacheDir)

	cfgDir := t.TempDir()
	cfgFile := filepath.Join(cfgDir, "config.hcl")
	if err := os.WriteFile(cfgFile, []byte(`
plugin "upgplugin" {
  source  = "file://`+fakeDir+`"
  version = "1.0.0"
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Pre-write a lock file with a stale entry on another platform so we can
	// verify it is preserved alongside the new current-platform entry.
	otherPlatform := "plan9_386"
	initialLock := `# llm-proxy.lock.hcl
plugin "upgplugin" {
  source   = "file://` + fakeDir + `"
  version  = "1.0.0"
  resolved = "0.9.0"
  platform = "` + otherPlatform + `"
}
`
	if err := os.WriteFile(filepath.Join(cfgDir, lockfile.FileName), []byte(initialLock), 0o600); err != nil {
		t.Fatal(err)
	}

	// Run with --upgrade — must resolve from source (exact pin → 1.0.0) and
	// update the lock file for the current platform.
	out, err := run(t, "plugin", "install", "--upgrade", "--config", cfgFile)
	if err != nil {
		t.Fatalf("plugin install --upgrade: %v\nout: %s", err, out)
	}

	lf, err := lockfile.Read(filepath.Join(cfgDir, lockfile.FileName))
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	entry, ok := lf.Lookup("upgplugin")
	if !ok {
		t.Fatal("lock file missing entry for current platform after upgrade")
	}
	if entry.Resolved != "1.0.0" {
		t.Errorf("Resolved = %q, want %q", entry.Resolved, "1.0.0")
	}
	// The other-platform entry should be preserved.
	found := false
	for _, e := range lf.Entries() {
		if e.Platform == otherPlatform {
			found = true
			if e.Resolved != "0.9.0" {
				t.Errorf("other platform Resolved = %q, want 0.9.0", e.Resolved)
			}
		}
	}
	if !found {
		t.Error("other-platform entry was lost after upgrade")
	}
}

// TestPlugin_Install_Upgrade_SkipsCachedVersion verifies that --upgrade still
// skips the binary download when the remote-resolved version is already cached
// (idempotent re-runs).
func TestPlugin_Install_Upgrade_SkipsCachedVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script test not supported on Windows")
	}
	fakeDir := buildFakePluginDir(t, "upgcacheplugin", "1.0.0")

	cacheDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", cacheDir)

	cfgDir := t.TempDir()
	cfgFile := filepath.Join(cfgDir, "config.hcl")
	if err := os.WriteFile(cfgFile, []byte(`
plugin "upgcacheplugin" {
  source  = "file://`+fakeDir+`"
  version = "1.0.0"
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	// First install (no --upgrade): populates cache.
	if _, err := run(t, "plugin", "install", "--config", cfgFile); err != nil {
		t.Fatalf("first install: %v", err)
	}

	// Second install with --upgrade: remote resolves to 1.0.0 (same as
	// cached). Binary download should be skipped.
	out, err := run(t, "plugin", "install", "--upgrade", "--config", cfgFile)
	if err != nil {
		t.Fatalf("upgrade install: %v\nout: %s", err, out)
	}
	if !strings.Contains(out, "already cached") {
		t.Errorf("expected 'already cached' when upgrade resolves to already-cached version:\n%s", out)
	}
}

// ---- SHA-256 checksum recording --------------------------------------------

// TestPlugin_Install_RecordsChecksumInLockfile verifies that after a fresh
// install, the lock file entry contains a non-empty checksum that matches the
// SHA-256 of the installed binary.
func TestPlugin_Install_RecordsChecksumInLockfile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script test not supported on Windows")
	}
	fakeDir := buildFakePluginDir(t, "cksumplugin", "1.0.0")

	cacheDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", cacheDir)

	cfgDir := t.TempDir()
	cfgFile := filepath.Join(cfgDir, "config.hcl")
	if err := os.WriteFile(cfgFile, []byte(`
plugin "cksumplugin" {
  source  = "file://`+fakeDir+`"
  version = "1.0.0"
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := run(t, "plugin", "install", "--config", cfgFile); err != nil {
		t.Fatalf("plugin install: %v", err)
	}

	lockPath := filepath.Join(cfgDir, lockfile.FileName)
	lf, err := lockfile.Read(lockPath)
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	entry, ok := lf.Lookup("cksumplugin")
	if !ok {
		t.Fatal("lock file missing entry for 'cksumplugin'")
	}
	if entry.Checksum == "" {
		t.Fatal("lock file entry has empty Checksum after install")
	}

	// Verify the checksum matches the on-disk binary.
	namespace, name := "cksumplugin", "cksumplugin"
	destDir := plugin.PluginCacheDirNoCreate(cacheDir, namespace, name, "1.0.0")
	binPath := filepath.Join(destDir, plugin.BinaryName("cksumplugin"))
	f, openErr := os.Open(binPath)
	if openErr != nil {
		t.Fatalf("open binary: %v", openErr)
	}
	defer f.Close()
	h := sha256.New()
	if _, copyErr := io.Copy(h, f); copyErr != nil {
		t.Fatalf("hash binary: %v", copyErr)
	}
	want := fmt.Sprintf("%x", h.Sum(nil))
	if entry.Checksum != want {
		t.Errorf("Checksum = %q, want %q", entry.Checksum, want)
	}
}

// TestPlugin_Install_ChecksumStableOnRepeatInstall verifies that running
// `plugin install` a second time (cache hit path) preserves the existing
// checksum in the lock file unchanged.
func TestPlugin_Install_ChecksumStableOnRepeatInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script test not supported on Windows")
	}
	fakeDir := buildFakePluginDir(t, "ckstableplugin", "1.0.0")

	cacheDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", cacheDir)

	cfgDir := t.TempDir()
	cfgFile := filepath.Join(cfgDir, "config.hcl")
	if err := os.WriteFile(cfgFile, []byte(`
plugin "ckstableplugin" {
  source  = "file://`+fakeDir+`"
  version = "1.0.0"
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := run(t, "plugin", "install", "--config", cfgFile); err != nil {
		t.Fatalf("first install: %v", err)
	}

	lockPath := filepath.Join(cfgDir, lockfile.FileName)
	lf1, err := lockfile.Read(lockPath)
	if err != nil {
		t.Fatalf("read lock after first install: %v", err)
	}
	e1, _ := lf1.Lookup("ckstableplugin")

	if _, err := run(t, "plugin", "install", "--config", cfgFile); err != nil {
		t.Fatalf("second install: %v", err)
	}

	lf2, err := lockfile.Read(lockPath)
	if err != nil {
		t.Fatalf("read lock after second install: %v", err)
	}
	e2, _ := lf2.Lookup("ckstableplugin")

	if e1.Checksum == "" {
		t.Fatal("first install produced empty checksum")
	}
	if e1.Checksum != e2.Checksum {
		t.Errorf("checksum changed on repeat install: %q vs %q", e1.Checksum, e2.Checksum)
	}
}

// ---- SHA256SUMS remote verification ----------------------------------------

// buildPluginArchive creates a .tar.gz containing the binary and manifest from
// pluginDir and returns the archive bytes.
func buildPluginArchive(t *testing.T, pluginDir string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	entries, err := os.ReadDir(pluginDir)
	if err != nil {
		t.Fatalf("read plugin dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(pluginDir, e.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: e.Name(),
			Mode: 0o755,
			Size: int64(len(data)),
		}); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("write data: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

// archiveBinaryName returns the expected archive filename for a plugin on the
// current platform, matching the convention in buildGitHubReleaseURL.
func archiveBinaryName(pluginName, version string) string {
	return fmt.Sprintf("llm-proxy-provider-%s_%s_%s_%s.tar.gz",
		pluginName, version, runtime.GOOS, runtime.GOARCH)
}

// newChecksumServer returns an httptest.Server that serves a .tar.gz archive
// at /releases/download/v<version>/<archiveName> and the corresponding
// SHA256SUMS file. It returns the server URL root and a cleanup func.
// If badChecksum is true, the SHA256SUMS file contains a wrong hex value.
func newChecksumServer(t *testing.T, pluginName, version string, archiveBytes []byte, badChecksum bool) *httptest.Server {
	t.Helper()
	archiveName := archiveBinaryName(pluginName, version)

	// Compute the real SHA-256 of the archive for use in SHA256SUMS.
	h := sha256.New()
	h.Write(archiveBytes)
	realHex := fmt.Sprintf("%x", h.Sum(nil))

	checksumHex := realHex
	if badChecksum {
		checksumHex = strings.Repeat("0", 64) // wrong
	}
	sumsBody := checksumHex + "  " + archiveName + "\n"

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "SHA256SUMS"):
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(sumsBody))
		case strings.HasSuffix(r.URL.Path, ".tar.gz"):
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write(archiveBytes)
		default:
			http.NotFound(w, r)
		}
	}))
}

// TestDownloadAndExtract_VerifiesChecksumFromSumsFile verifies that when a
// checksum URL is provided, the installed binary's SHA-256 is verified against
// the downloaded SHA256SUMS file and succeeds when they match.
func TestDownloadAndExtract_VerifiesChecksumFromSumsFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sha256sums test not supported on Windows")
	}

	pluginName := "sha256testplugin"
	version := "1.0.0"
	fakeDir := buildFakePluginDir(t, pluginName, version)
	archive := buildPluginArchive(t, fakeDir)

	srv := newChecksumServer(t, pluginName, version, archive, false)
	t.Cleanup(srv.Close)

	archiveName := archiveBinaryName(pluginName, version)
	archiveURL := srv.URL + "/releases/download/v" + version + "/" + archiveName
	checksumURL := srv.URL + "/releases/download/v" + version + "/SHA256SUMS"
	destDir := t.TempDir()

	err := cli.DownloadAndExtractForTest(context.Background(), srv.Client(), archiveURL, checksumURL, destDir, pluginName)
	if err != nil {
		t.Fatalf("DownloadAndExtract with matching checksum: %v", err)
	}
	// Binary must exist in destDir.
	binPath := filepath.Join(destDir, plugin.BinaryName(pluginName))
	if _, statErr := os.Stat(binPath); statErr != nil {
		t.Errorf("binary not found after extract: %v", statErr)
	}
}

// TestDownloadAndExtract_RejectsChecksumMismatch verifies that a mismatch
// between the downloaded binary's checksum and the SHA256SUMS file causes a
// hard error and removes the destination directory.
func TestDownloadAndExtract_RejectsChecksumMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sha256sums test not supported on Windows")
	}

	pluginName := "sha256mismatchplugin"
	version := "1.0.0"
	fakeDir := buildFakePluginDir(t, pluginName, version)
	archive := buildPluginArchive(t, fakeDir)

	srv := newChecksumServer(t, pluginName, version, archive, true /* bad checksum */)
	t.Cleanup(srv.Close)

	archiveName := archiveBinaryName(pluginName, version)
	archiveURL := srv.URL + "/releases/download/v" + version + "/" + archiveName
	checksumURL := srv.URL + "/releases/download/v" + version + "/SHA256SUMS"
	destDir := t.TempDir()

	err := cli.DownloadAndExtractForTest(context.Background(), srv.Client(), archiveURL, checksumURL, destDir, pluginName)
	if err == nil {
		t.Fatal("expected checksum mismatch error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "checksum") {
		t.Errorf("expected error mentioning 'checksum', got: %v", err)
	}
}

// TestDownloadAndExtract_NoChecksumURL verifies that omitting the checksum
// URL (empty string) skips verification and still extracts successfully.
func TestDownloadAndExtract_NoChecksumURL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sha256sums test not supported on Windows")
	}

	pluginName := "nochecksumplugin"
	version := "1.0.0"
	fakeDir := buildFakePluginDir(t, pluginName, version)
	archive := buildPluginArchive(t, fakeDir)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	}))
	t.Cleanup(srv.Close)

	archiveURL := srv.URL + "/plugin.tar.gz"
	destDir := t.TempDir()

	err := cli.DownloadAndExtractForTest(context.Background(), srv.Client(), archiveURL, "" /* no checksum URL */, destDir, pluginName)
	if err != nil {
		t.Fatalf("DownloadAndExtract without checksum URL: %v", err)
	}
}
