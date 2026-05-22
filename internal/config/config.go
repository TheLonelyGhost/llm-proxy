// Package config defines the on-disk HCL2 configuration schema and the loader
// that reads it from either a single *.hcl file or a directory of them.
//
// All secret-bearing fields support env-var interpolation via the HCL2
// function env("NAME"). This lets users avoid committing tokens to disk.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// Config is the top-level configuration document.
type Config struct {
	Server      *ServerBlock   `hcl:"server,block"`
	Backends    []BackendBlock `hcl:"backend,block"`
	Remain      hcl.Body       `hcl:",remain"`
	SourcePath  string         // Set by Load() for debugging/logging
	SourceFiles []string       // Set by Load() for debugging/logging
}

// ServerBlock configures the HTTP listener.
type ServerBlock struct {
	Address       string  `hcl:"address,optional"`
	Port          int     `hcl:"port,optional"`
	Tracing       bool    `hcl:"tracing,optional"`
	TraceExporter string  `hcl:"trace_exporter,optional"`
	TraceSampler  string  `hcl:"trace_sampler,optional"`
	TraceRatio    float64 `hcl:"trace_ratio,optional"`
	TraceService  string  `hcl:"trace_service,optional"`
}

// BackendBlock is a generic provider configuration. The block label is the
// routing prefix (matching Backend.Name()); the inner body is decoded by the
// provider plugin itself, so each provider can define its own attributes.
type BackendBlock struct {
	// Name is the block label, used as the routing prefix.
	Name string `hcl:",label"`
	// Type names the provider implementation to load (e.g. "github-copilot").
	Type string `hcl:"type"`
	// Models is an optional allow-list. If empty the backend exposes
	// whatever its provider reports.
	Models []string `hcl:"models,optional"`
	// Remain is decoded by the provider plugin.
	Remain hcl.Body `hcl:",remain"`
}

// Defaults applied to a freshly loaded Config when fields are unset.
const (
	DefaultAddress = "127.0.0.1"
	DefaultPort    = 4980
)

// ApplyDefaults fills in any unset server fields with secure defaults.
func (c *Config) ApplyDefaults() {
	if c.Server == nil {
		c.Server = &ServerBlock{}
	}
	if c.Server.Address == "" {
		c.Server.Address = DefaultAddress
	}
	if c.Server.Port == 0 {
		c.Server.Port = DefaultPort
	}
}

// Validate enforces invariants beyond what HCL syntax can express.
func (c *Config) Validate() error {
	if c.Server != nil {
		if c.Server.Port < 0 || c.Server.Port > 65535 {
			return fmt.Errorf("server.port %d out of range", c.Server.Port)
		}
	}
	seen := make(map[string]struct{}, len(c.Backends))
	for i, b := range c.Backends {
		if b.Name == "" {
			return fmt.Errorf("backend[%d]: empty label", i)
		}
		if strings.ContainsRune(b.Name, '/') {
			return fmt.Errorf("backend %q: label may not contain '/'", b.Name)
		}
		if b.Name != strings.ToLower(b.Name) {
			return fmt.Errorf("backend %q: label must be lowercase", b.Name)
		}
		if b.Type == "" {
			return fmt.Errorf("backend %q: type is required", b.Name)
		}
		if _, dup := seen[b.Name]; dup {
			return fmt.Errorf("backend %q: duplicate label", b.Name)
		}
		seen[b.Name] = struct{}{}
	}
	return nil
}

// EvalContext returns the HCL evaluation context used both for the top-level
// decode and for plugin-specific decoding of BackendBlock.Remain.
//
// It exposes env("NAME") as the only function. We deliberately omit the
// filesystem-reading helpers HCL provides by default to limit attack surface.
func EvalContext() *hcl.EvalContext {
	return &hcl.EvalContext{
		Functions: map[string]function.Function{
			"env": envFunc,
		},
	}
}

var envFunc = function.New(&function.Spec{
	Params: []function.Parameter{{
		Name: "name",
		Type: cty.String,
	}},
	Type: function.StaticReturnType(cty.String),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		name := args[0].AsString()
		return cty.StringVal(os.Getenv(name)), nil
	},
})

// Load reads configuration from path, which may be a single *.hcl file or a
// directory containing one or more *.hcl files. The files are merged in
// lexicographic order; on conflicts the loader returns an error rather than
// silently overwriting.
func Load(path string) (*Config, error) {
	files, err := discover(path)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no .hcl files found at %q", path)
	}
	parser := hclparse.NewParser()
	bodies := make([]hcl.Body, 0, len(files))
	for _, f := range files {
		data, err := os.ReadFile(f) //nolint:gosec // user-supplied config path is intended
		if err != nil {
			return nil, fmt.Errorf("read %q: %w", f, err)
		}
		file, diags := parser.ParseHCL(data, f)
		if diags.HasErrors() {
			return nil, fmt.Errorf("parse %q: %s", f, diags.Error())
		}
		if file == nil {
			return nil, fmt.Errorf("parse %q: nil file", f)
		}
		bodies = append(bodies, file.Body)
	}
	merged := hcl.MergeBodies(bodies)

	cfg := &Config{}
	if diags := gohcl.DecodeBody(merged, EvalContext(), cfg); diags.HasErrors() {
		return nil, fmt.Errorf("decode config: %s", diags.Error())
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	cfg.SourcePath = path
	cfg.SourceFiles = files

	return cfg, nil
}

// DecodeBackend decodes a BackendBlock's provider-specific body into target.
// Plugins call this after the registry hands them a BackendBlock.
func DecodeBackend(b BackendBlock, target any) error {
	if diags := gohcl.DecodeBody(b.Remain, EvalContext(), target); diags.HasErrors() {
		return fmt.Errorf("decode backend %q: %s", b.Name, diags.Error())
	}
	return nil
}

func discover(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %q: %w", path, err)
	}
	if !info.IsDir() {
		if filepath.Ext(path) != ".hcl" {
			return nil, fmt.Errorf("config file %q must end in .hcl", path)
		}
		return []string{path}, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("read dir %q: %w", path, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if filepath.Ext(name) != ".hcl" {
			continue
		}
		out = append(out, filepath.Join(path, name))
	}
	sort.Strings(out)
	return out, nil
}
