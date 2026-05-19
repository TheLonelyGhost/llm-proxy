package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thelonelyghost/llm-proxy/internal/cli"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "c.hcl")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	root := cli.New(&buf)
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}

func TestVersion(t *testing.T) {
	out, err := run(t, "version")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "llm-proxy") {
		t.Errorf("out = %q", out)
	}
}

func TestServe_MissingConfig(t *testing.T) {
	t.Setenv("LLM_PROXY_CONFIG", filepath.Join(t.TempDir(), "missing.hcl"))
	_, err := run(t, "serve")
	if err == nil {
		t.Fatal("expected error for missing default config")
	}
	if !strings.Contains(err.Error(), "missing.hcl") {
		t.Fatalf("err = %v", err)
	}
}

func TestServe_UnknownBackendType(t *testing.T) {
	cfg := writeConfig(t, `
backend "broken" {
  type = "no-such-provider"
}
`)
	_, err := run(t, "serve", "-c", cfg)
	if err == nil || !strings.Contains(err.Error(), "unknown type") {
		t.Fatalf("err = %v", err)
	}
}

func TestServe_NoBackends(t *testing.T) {
	cfg := writeConfig(t, `server { port = 5000 }`)
	_, err := run(t, "serve", "-c", cfg)
	if err == nil || !strings.Contains(err.Error(), "no backends") {
		t.Fatalf("err = %v", err)
	}
}

func TestLogin_NoCopilotBlock(t *testing.T) {
	// Config has no github-copilot backend - login should refuse.
	cfg := writeConfig(t, `server { port = 5000 }`)
	_, err := run(t, "login", "-c", cfg)
	if err == nil || !strings.Contains(err.Error(), "no backend of type") {
		t.Fatalf("err = %v", err)
	}
}

func TestLogout_NoCopilotBlock(t *testing.T) {
	cfg := writeConfig(t, `server { port = 5000 }`)
	_, err := run(t, "logout", "-c", cfg)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLogin_BadConfig(t *testing.T) {
	_, err := run(t, "login", "-c", filepath.Join(t.TempDir(), "missing.hcl"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestServe_ConfigParseError(t *testing.T) {
	cfg := writeConfig(t, `not valid hcl @@@`)
	_, err := run(t, "serve", "-c", cfg)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestIBMBobExtract_NoArgs_NoBobOnPath(t *testing.T) {
	// Force an empty PATH so exec.LookPath("bob") fails deterministically.
	t.Setenv("PATH", "")
	_, err := run(t, "ibm-bob", "extract")
	if err == nil || !strings.Contains(err.Error(), "not found on $PATH") {
		t.Fatalf("err = %v", err)
	}
}

func TestIBMBobExtract_PathDoesNotExist(t *testing.T) {
	_, err := run(t, "ibm-bob", "extract", filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestIBMBobExtract_HappyPath_JSONAndEnv(t *testing.T) {
	// Construct a real fixture bundle that the extractor can decrypt.
	// We mirror synthBundle from the ibmbob package, inline, so we do not
	// have to export test helpers across package boundaries.
	bundle, wantVersion, wantSecret := buildFixtureBundle(t)
	path := filepath.Join(t.TempDir(), "bob.js")
	if err := os.WriteFile(path, bundle, 0o600); err != nil {
		t.Fatal(err)
	}

	// JSON output (default).
	out, err := run(t, "ibm-bob", "extract", path)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, `"version": "`+wantVersion+`"`) {
		t.Errorf("missing version in output:\n%s", out)
	}
	if !strings.Contains(out, `"build_time_hmac_secret": "`+wantSecret+`"`) {
		t.Errorf("missing secret in output:\n%s", out)
	}

	// --env output.
	out, err = run(t, "ibm-bob", "extract", "--env", path)
	if err != nil {
		t.Fatalf("run --env: %v", err)
	}
	if !strings.Contains(out, "export BOB_VERSION=") || !strings.Contains(out, wantVersion) {
		t.Errorf("--env missing version: %q", out)
	}
	if !strings.Contains(out, "export BOB_HMAC_SECRET=") || !strings.Contains(out, wantSecret) {
		t.Errorf("--env missing secret: %q", out)
	}

	// --output to a file.
	dest := filepath.Join(t.TempDir(), "out.json")
	_, err = run(t, "ibm-bob", "extract", "-o", dest, path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", info.Mode().Perm())
	}
	body, _ := os.ReadFile(dest) //nolint:gosec
	if !strings.Contains(string(body), wantSecret) {
		t.Errorf("file does not contain secret")
	}
}

func TestIBMBobExtract_TooManyArgs(t *testing.T) {
	_, err := run(t, "ibm-bob", "extract", "a", "b")
	if err == nil {
		t.Fatal("expected error for too many args")
	}
}
