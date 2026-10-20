package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thelonelyghost/llm-proxy/internal/config"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad_SingleFile(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "config.hcl", `
server {
  address = "0.0.0.0"
  port    = 9999
}

backend "copilot" {
  type   = "github-copilot"
  models = ["gpt-4o", "claude-3.5-sonnet"]
}
`)
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Address != "0.0.0.0" || cfg.Server.Port != 9999 {
		t.Errorf("server = %+v", cfg.Server)
	}
	if len(cfg.Backends) != 1 || cfg.Backends[0].Name != "copilot" {
		t.Errorf("backends = %+v", cfg.Backends)
	}
	if cfg.Backends[0].Type != "github-copilot" {
		t.Errorf("type = %q", cfg.Backends[0].Type)
	}
	if len(cfg.Backends[0].Models) != 2 {
		t.Errorf("models = %v", cfg.Backends[0].Models)
	}
	if cfg.SourcePath != p {
		t.Errorf("SourcePath = %q, want %q", cfg.SourcePath, p)
	}
	if len(cfg.SourceFiles) != 1 || cfg.SourceFiles[0] != p {
		t.Errorf("SourceFiles = %v, want [%q]", cfg.SourceFiles, p)
	}
}

func TestLoad_Directory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "01-server.hcl", `
server { port = 5000 }
`)
	writeFile(t, dir, "02-backend.hcl", `
backend "copilot" {
  type = "github-copilot"
}
`)
	writeFile(t, dir, ".hidden.hcl", `bogus`) // hidden -> ignored
	writeFile(t, dir, "README.md", `not hcl`) // wrong ext -> ignored
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != 5000 {
		t.Errorf("port = %d", cfg.Server.Port)
	}
	if cfg.Server.Address != config.DefaultAddress {
		t.Errorf("default address not applied: %q", cfg.Server.Address)
	}
	if len(cfg.Backends) != 1 {
		t.Fatalf("backends = %v", cfg.Backends)
	}
	if cfg.SourcePath != dir {
		t.Errorf("SourcePath = %q, want %q", cfg.SourcePath, dir)
	}
	if len(cfg.SourceFiles) != 2 {
		t.Errorf("SourceFiles = %v, expected 2 files", cfg.SourceFiles)
	}
	for _, f := range cfg.SourceFiles {
		if !strings.HasPrefix(f, dir) {
			t.Errorf("SourceFile %q doesn't start with dir %q", f, dir)
		}
		if !strings.HasSuffix(f, ".hcl") {
			t.Errorf("SourceFile %q doesn't end with .hcl", f)
		}
	}
}

func TestLoad_Defaults(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "c.hcl", `
backend "copilot" { type = "github-copilot" }
`)
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Address != config.DefaultAddress {
		t.Errorf("addr = %q", cfg.Server.Address)
	}
	if cfg.Server.Port != config.DefaultPort {
		t.Errorf("port = %d", cfg.Server.Port)
	}
}

func TestLoad_EnvFunc(t *testing.T) {
	t.Setenv("LLMPROXY_TEST_TOKEN", "secret123")
	dir := t.TempDir()
	p := writeFile(t, dir, "c.hcl", `
backend "copilot" {
  type   = "github-copilot"
  models = [env("LLMPROXY_TEST_TOKEN")]
}
`)
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Backends[0].Models[0] != "secret123" {
		t.Errorf("env interpolation failed: %v", cfg.Backends[0].Models)
	}
}

func TestLoad_Errors(t *testing.T) {
	t.Run("missing path", func(t *testing.T) {
		if _, err := config.Load(filepath.Join(t.TempDir(), "nope.hcl")); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("empty dir", func(t *testing.T) {
		dir := t.TempDir()
		_, err := config.Load(dir)
		if err == nil || !strings.Contains(err.Error(), "no .hcl files") {
			t.Fatalf("want no-files error, got %v", err)
		}
	})
	t.Run("wrong ext", func(t *testing.T) {
		dir := t.TempDir()
		p := writeFile(t, dir, "c.txt", `server {}`)
		if _, err := config.Load(p); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("syntax error", func(t *testing.T) {
		dir := t.TempDir()
		p := writeFile(t, dir, "c.hcl", `server { port = `)
		if _, err := config.Load(p); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("duplicate backend", func(t *testing.T) {
		dir := t.TempDir()
		p := writeFile(t, dir, "c.hcl", `
backend "copilot" { type = "github-copilot" }
backend "copilot" { type = "github-copilot" }
`)
		if _, err := config.Load(p); err == nil {
			t.Fatal("expected duplicate error")
		}
	})
	t.Run("missing type", func(t *testing.T) {
		dir := t.TempDir()
		p := writeFile(t, dir, "c.hcl", `backend "copilot" {}`)
		if _, err := config.Load(p); err == nil {
			t.Fatal("expected missing-type error")
		}
	})
	t.Run("uppercase label", func(t *testing.T) {
		dir := t.TempDir()
		p := writeFile(t, dir, "c.hcl", `backend "Copilot" { type = "github-copilot" }`)
		if _, err := config.Load(p); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("port out of range", func(t *testing.T) {
		dir := t.TempDir()
		p := writeFile(t, dir, "c.hcl", `server { port = 70000 }`)
		if _, err := config.Load(p); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestDecodeBackend(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "c.hcl", `
backend "copilot" {
  type        = "github-copilot"
  api_base    = "https://api.example.test"
  request_timeout = "30s"
}
`)
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	var sub struct {
		APIBase        string `hcl:"api_base,optional"`
		RequestTimeout string `hcl:"request_timeout,optional"`
	}
	if err := config.DecodeBackend(cfg.Backends[0], &sub); err != nil {
		t.Fatalf("DecodeBackend: %v", err)
	}
	if sub.APIBase != "https://api.example.test" {
		t.Errorf("api_base = %q", sub.APIBase)
	}
	if sub.RequestTimeout != "30s" {
		t.Errorf("timeout = %q", sub.RequestTimeout)
	}
}

func TestBackendConfigJSON(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "c.hcl", `
backend "mybackend" {
  type     = "myplugin"
  api_base = "https://example.test"
  models   = ["gpt-4o"]
}
`)
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}

	got, err := config.BackendConfigJSON(cfg.Backends[0])
	if err != nil {
		t.Fatalf("BackendConfigJSON: %v", err)
	}

	for _, want := range []string{`"name"`, `"mybackend"`, `"type"`, `"myplugin"`, `"api_base"`, `"https://example.test"`} {
		if !strings.Contains(got, want) {
			t.Errorf("BackendConfigJSON missing %s: %s", want, got)
		}
	}
}

// TestBackendConfigJSON_ComplexAttributes verifies that list, map, object and
// null attributes are encoded as plain JSON values, not Go syntax.
func TestBackendConfigJSON_ComplexAttributes(t *testing.T) {
	cases := []struct {
		name string
		attr string
		want any
	}{
		{
			name: "list",
			attr: `allowed = ["a", "b"]`,
			want: []any{"a", "b"},
		},
		{
			name: "map",
			attr: `headers = { "X-Team" = "llm", retries = 2 }`,
			want: map[string]any{"X-Team": "llm", "retries": float64(2)},
		},
		{
			name: "nested object in list",
			attr: `routes = [{ name = "a", weight = 1.5, on = true }]`,
			want: []any{map[string]any{"name": "a", "weight": 1.5, "on": true}},
		},
		{
			name: "null",
			attr: `extra = null`,
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := writeFile(t, t.TempDir(), "c.hcl", `
backend "mybackend" {
  type = "myplugin"
  `+tc.attr+`
}
`)
			cfg, err := config.Load(p)
			if err != nil {
				t.Fatal(err)
			}

			raw, err := config.BackendConfigJSON(cfg.Backends[0])
			if err != nil {
				t.Fatalf("BackendConfigJSON: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(raw), &got); err != nil {
				t.Fatalf("output is not valid JSON: %v\n%s", err, raw)
			}
			key, _, _ := strings.Cut(tc.attr, " ")
			val, ok := got[key]
			if !ok {
				t.Fatalf("key %q missing: %s", key, raw)
			}
			if !reflect.DeepEqual(val, tc.want) {
				t.Errorf("%s = %#v, want %#v", key, val, tc.want)
			}
		})
	}
}

func FuzzLoadHCL(f *testing.F) {
	seeds := []string{
		`server { port = 1 }`,
		`backend "x" { type = "y" }`,
		`backend "x" { type = "y" models = ["a","b"] }`,
		`# comment`,
		``,
		`server { address = env("FOO") }`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, body string) {
		// Skip pathologically large inputs to keep the fuzzer brisk.
		if len(body) > 8192 {
			t.Skip()
		}
		p := filepath.Join(dir, "fuzz.hcl")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		// Loader must never panic on arbitrary input; returning an
		// error is a perfectly acceptable outcome.
		_, _ = config.Load(p)
	})
}
