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
	root := cli.New(&buf, &buf)
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

func TestIBMBobCommand_Removed(t *testing.T) {
	// ibm-bob subcommand is removed; it is now a plugin tooling subcommand.
	_, err := run(t, "ibm-bob", "extract")
	if err == nil {
		t.Fatal("expected error: ibm-bob command should not exist")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("expected 'unknown command' error, got: %v", err)
	}
}

func TestServe_ConfigParseError(t *testing.T) {
	cfg := writeConfig(t, `not valid hcl @@@`)
	_, err := run(t, "serve", "-c", cfg)
	if err == nil {
		t.Fatal("expected error")
	}
}
