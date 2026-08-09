package config_test

import (
	"strings"
	"testing"

	"github.com/thelonelyghost/llm-proxy/internal/config"
)

// TestLoad_PluginBlock verifies that a valid plugin block is decoded correctly.
func TestLoad_PluginBlock(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "config.hcl", `
plugin "ibmbob" {
  source  = "github.com/thelonelyghost/llm-proxy-provider-ibmbob"
  version = "~> 1.0"
}

backend "work" {
  type = "ibmbob"
}
`)
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Plugins) != 1 {
		t.Fatalf("len(Plugins) = %d, want 1", len(cfg.Plugins))
	}
	pb := cfg.Plugins[0]
	if pb.Name != "ibmbob" {
		t.Errorf("Name = %q, want %q", pb.Name, "ibmbob")
	}
	if pb.Source != "github.com/thelonelyghost/llm-proxy-provider-ibmbob" {
		t.Errorf("Source = %q", pb.Source)
	}
	if pb.Version != "~> 1.0" {
		t.Errorf("Version = %q, want %q", pb.Version, "~> 1.0")
	}
}

// TestLoad_PluginBlock_OptionalLifecycleFields verifies that optional lifecycle
// tuning fields decode correctly.
func TestLoad_PluginBlock_OptionalLifecycleFields(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "config.hcl", `
plugin "ibmbob" {
  source  = "github.com/thelonelyghost/llm-proxy-provider-ibmbob"
  version = "1.2.0"

  health_interval  = "5s"
  health_timeout   = "3s"
  health_threshold = 5

  restart_policy          = "always"
  restart_max_retries     = 10
  restart_initial_backoff = "2s"
  restart_backoff_cap     = "30s"
}
`)
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Plugins) != 1 {
		t.Fatalf("len(Plugins) = %d, want 1", len(cfg.Plugins))
	}
	pb := cfg.Plugins[0]
	if pb.HealthInterval != "5s" {
		t.Errorf("HealthInterval = %q, want %q", pb.HealthInterval, "5s")
	}
	if pb.HealthTimeout != "3s" {
		t.Errorf("HealthTimeout = %q, want %q", pb.HealthTimeout, "3s")
	}
	if pb.HealthThreshold != 5 {
		t.Errorf("HealthThreshold = %d, want 5", pb.HealthThreshold)
	}
	if pb.RestartPolicy != "always" {
		t.Errorf("RestartPolicy = %q, want %q", pb.RestartPolicy, "always")
	}
	if pb.RestartMaxRetries != 10 {
		t.Errorf("RestartMaxRetries = %d, want 10", pb.RestartMaxRetries)
	}
	if pb.RestartInitialBackoff != "2s" {
		t.Errorf("RestartInitialBackoff = %q, want %q", pb.RestartInitialBackoff, "2s")
	}
	if pb.RestartBackoffCap != "30s" {
		t.Errorf("RestartBackoffCap = %q, want %q", pb.RestartBackoffCap, "30s")
	}
}

// TestLoad_PluginBlock_DuplicateLabel verifies that duplicate plugin labels
// are rejected.
func TestLoad_PluginBlock_DuplicateLabel(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "config.hcl", `
plugin "ibmbob" { source = "github.com/a/b" version = "1.0.0" }
plugin "ibmbob" { source = "github.com/a/b" version = "1.0.0" }
`)
	_, err := config.Load(p)
	if err == nil {
		t.Fatal("expected duplicate plugin label error, got nil")
	}
}

// TestLoad_PluginBlock_MissingSource verifies that a plugin block without a
// source field is rejected.
func TestLoad_PluginBlock_MissingSource(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "config.hcl", `
plugin "ibmbob" { version = "1.0.0" }
`)
	_, err := config.Load(p)
	if err == nil {
		t.Fatal("expected error for missing source, got nil")
	}
	if !strings.Contains(err.Error(), "source") {
		t.Errorf("error should mention 'source', got: %v", err)
	}
}

// TestLoad_PluginBlock_MissingVersion verifies that a plugin block without a
// version field is rejected.
func TestLoad_PluginBlock_MissingVersion(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "config.hcl", `
plugin "ibmbob" { source = "github.com/a/b" }
`)
	_, err := config.Load(p)
	if err == nil {
		t.Fatal("expected error for missing version, got nil")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Errorf("error should mention 'version', got: %v", err)
	}
}

// TestLoad_ServerShutdownTimeout verifies that shutdown_timeout on the server
// block decodes correctly.
func TestLoad_ServerShutdownTimeout(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "config.hcl", `
server { shutdown_timeout = "45s" }
`)
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.ShutdownTimeout != "45s" {
		t.Errorf("ShutdownTimeout = %q, want %q", cfg.Server.ShutdownTimeout, "45s")
	}
}
