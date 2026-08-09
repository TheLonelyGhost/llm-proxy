package plugin_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/thelonelyghost/llm-proxy/internal/plugin"
)

// TestParseManifest_Valid verifies that a well-formed manifest JSON round-trips
// correctly into a Manifest value.
func TestParseManifest_Valid(t *testing.T) {
	raw := `{
		"name": "ibmbob",
		"version": "1.2.0",
		"protocol_version": 1,
		"port_env_var": "LLM_PROXY_PLUGIN_PORT",
		"endpoints": {
			"health": "/healthz",
			"reload": "/management/reload"
		},
		"tools": [
			{"name": "login",   "description": "Authenticate via browser SSO"},
			{"name": "extract", "description": "Extract HMAC secret from bob CLI"}
		]
	}`
	m, err := plugin.ParseManifest([]byte(raw))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if m.Name != "ibmbob" {
		t.Errorf("Name = %q, want %q", m.Name, "ibmbob")
	}
	if m.Version != "1.2.0" {
		t.Errorf("Version = %q, want %q", m.Version, "1.2.0")
	}
	if m.ProtocolVersion != 1 {
		t.Errorf("ProtocolVersion = %d, want 1", m.ProtocolVersion)
	}
	if m.PortEnvVar != "LLM_PROXY_PLUGIN_PORT" {
		t.Errorf("PortEnvVar = %q, want %q", m.PortEnvVar, "LLM_PROXY_PLUGIN_PORT")
	}
	if m.Endpoints.Health != "/healthz" {
		t.Errorf("Endpoints.Health = %q, want %q", m.Endpoints.Health, "/healthz")
	}
	if m.Endpoints.Reload != "/management/reload" {
		t.Errorf("Endpoints.Reload = %q, want %q", m.Endpoints.Reload, "/management/reload")
	}
	if len(m.Tools) != 2 {
		t.Fatalf("len(Tools) = %d, want 2", len(m.Tools))
	}
	if m.Tools[0].Name != "login" {
		t.Errorf("Tools[0].Name = %q, want %q", m.Tools[0].Name, "login")
	}
}

// TestParseManifest_MissingName verifies that a manifest without a name is rejected.
func TestParseManifest_MissingName(t *testing.T) {
	raw := `{"version":"1.0.0","protocol_version":1,"port_env_var":"LLM_PROXY_PLUGIN_PORT","endpoints":{"health":"/healthz","reload":"/management/reload"}}`
	_, err := plugin.ParseManifest([]byte(raw))
	if err == nil {
		t.Fatal("expected error for missing name, got nil")
	}
	if !errors.Is(err, plugin.ErrInvalidManifest) {
		t.Errorf("expected ErrInvalidManifest, got %v", err)
	}
}

// TestParseManifest_MissingVersion verifies that a manifest without a version is rejected.
func TestParseManifest_MissingVersion(t *testing.T) {
	raw := `{"name":"ibmbob","protocol_version":1,"port_env_var":"LLM_PROXY_PLUGIN_PORT","endpoints":{"health":"/healthz","reload":"/management/reload"}}`
	_, err := plugin.ParseManifest([]byte(raw))
	if err == nil {
		t.Fatal("expected error for missing version, got nil")
	}
	if !errors.Is(err, plugin.ErrInvalidManifest) {
		t.Errorf("expected ErrInvalidManifest, got %v", err)
	}
}

// TestParseManifest_ZeroProtocolVersion verifies that protocol_version=0 is rejected.
func TestParseManifest_ZeroProtocolVersion(t *testing.T) {
	raw := `{"name":"ibmbob","version":"1.0.0","protocol_version":0,"port_env_var":"LLM_PROXY_PLUGIN_PORT","endpoints":{"health":"/healthz","reload":"/management/reload"}}`
	_, err := plugin.ParseManifest([]byte(raw))
	if err == nil {
		t.Fatal("expected error for zero protocol_version, got nil")
	}
	if !errors.Is(err, plugin.ErrInvalidManifest) {
		t.Errorf("expected ErrInvalidManifest, got %v", err)
	}
}

// TestParseManifest_UnsupportedProtocolVersion verifies that a protocol_version
// higher than the supported maximum is rejected with ErrUnsupportedProtocol.
func TestParseManifest_UnsupportedProtocolVersion(t *testing.T) {
	raw := `{"name":"ibmbob","version":"1.0.0","protocol_version":99,"port_env_var":"LLM_PROXY_PLUGIN_PORT","endpoints":{"health":"/healthz","reload":"/management/reload"}}`
	_, err := plugin.ParseManifest([]byte(raw))
	if err == nil {
		t.Fatal("expected error for unsupported protocol_version, got nil")
	}
	if !errors.Is(err, plugin.ErrUnsupportedProtocol) {
		t.Errorf("expected ErrUnsupportedProtocol, got %v", err)
	}
}

// TestParseManifest_InvalidJSON verifies that malformed JSON returns a parse error.
func TestParseManifest_InvalidJSON(t *testing.T) {
	_, err := plugin.ParseManifest([]byte(`{not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Errorf("expected json.SyntaxError, got %T: %v", err, err)
	}
}

// TestParseManifest_DefaultEndpoints verifies that missing endpoints fields
// receive their default values.
func TestParseManifest_DefaultEndpoints(t *testing.T) {
	raw := `{"name":"myplugin","version":"1.0.0","protocol_version":1,"port_env_var":"LLM_PROXY_PLUGIN_PORT"}`
	m, err := plugin.ParseManifest([]byte(raw))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if m.Endpoints.Health != "/healthz" {
		t.Errorf("Endpoints.Health = %q, want default %q", m.Endpoints.Health, "/healthz")
	}
	if m.Endpoints.Reload != "/management/reload" {
		t.Errorf("Endpoints.Reload = %q, want default %q", m.Endpoints.Reload, "/management/reload")
	}
}

// TestParseManifest_DefaultPortEnvVar verifies that a missing port_env_var
// receives the default value.
func TestParseManifest_DefaultPortEnvVar(t *testing.T) {
	raw := `{"name":"myplugin","version":"1.0.0","protocol_version":1}`
	m, err := plugin.ParseManifest([]byte(raw))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if m.PortEnvVar != "LLM_PROXY_PLUGIN_PORT" {
		t.Errorf("PortEnvVar = %q, want default %q", m.PortEnvVar, "LLM_PROXY_PLUGIN_PORT")
	}
}
