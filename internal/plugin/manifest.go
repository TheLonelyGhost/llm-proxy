// Package plugin provides types and utilities for the llm-proxy plugin system.
//
// Plugins are out-of-process HTTP sidecars that implement the OpenAI-compatible
// API surface. Each plugin ships a manifest file describing its name, version,
// protocol contract, and available tooling subcommands.
package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
)

// MaxProtocolVersion is the highest plugin protocol version this build of
// llm-proxy supports. A manifest declaring a higher version is rejected with
// ErrUnsupportedProtocol.
const MaxProtocolVersion = 1

// DefaultPortEnvVar is the environment variable llm-proxy sets to pass the
// assigned port to a plugin process.
const DefaultPortEnvVar = "LLM_PROXY_PLUGIN_PORT"

// ErrInvalidManifest is returned when a manifest is missing required fields.
var ErrInvalidManifest = errors.New("invalid plugin manifest")

// ErrUnsupportedProtocol is returned when a manifest's protocol_version
// exceeds MaxProtocolVersion.
var ErrUnsupportedProtocol = errors.New("unsupported plugin protocol version")

// Endpoints holds the HTTP paths for the plugin's management endpoints.
type Endpoints struct {
	// Health is the path polled for readiness and liveness checks.
	// Defaults to "/healthz".
	Health string `json:"health"`
	// Reload is the path called on SIGHUP to trigger a credential reload.
	// Defaults to "/management/reload".
	Reload string `json:"reload"`
}

// Tool describes a single tooling subcommand the plugin binary exposes.
type Tool struct {
	// Name is the subcommand name passed to the plugin via --tool <name>.
	Name string `json:"name"`
	// Description is a one-line human-readable description for help output.
	Description string `json:"description"`
}

// Manifest is the static descriptor shipped in a plugin archive alongside the
// binary. It is read by llm-proxy at install time and cached.
type Manifest struct {
	// Name is the plugin's canonical name (matches the plugin HCL block label).
	Name string `json:"name"`
	// Version is the plugin's release version string.
	Version string `json:"version"`
	// ProtocolVersion is the plugin protocol contract version. llm-proxy
	// hard-fails if this exceeds MaxProtocolVersion.
	ProtocolVersion int `json:"protocol_version"`
	// PortEnvVar is the environment variable the plugin reads to discover its
	// assigned port. Defaults to DefaultPortEnvVar.
	PortEnvVar string `json:"port_env_var"`
	// Endpoints holds overridable management endpoint paths.
	Endpoints Endpoints `json:"endpoints"`
	// Tools lists the tooling subcommands this plugin binary exposes.
	Tools []Tool `json:"tools,omitempty"`
}

// ParseManifest decodes and validates a manifest from raw JSON bytes.
//
// It returns ErrInvalidManifest (wrapped) when required fields are absent, and
// ErrUnsupportedProtocol (wrapped) when the protocol_version exceeds
// MaxProtocolVersion.
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}

	// Apply defaults before validation so callers see the resolved values.
	if m.PortEnvVar == "" {
		m.PortEnvVar = DefaultPortEnvVar
	}
	if m.Endpoints.Health == "" {
		m.Endpoints.Health = "/healthz"
	}
	if m.Endpoints.Reload == "" {
		m.Endpoints.Reload = "/management/reload"
	}

	// Validate required fields.
	if m.Name == "" {
		return nil, fmt.Errorf("%w: name is required", ErrInvalidManifest)
	}
	if m.Version == "" {
		return nil, fmt.Errorf("%w: version is required", ErrInvalidManifest)
	}
	if m.ProtocolVersion == 0 {
		return nil, fmt.Errorf("%w: protocol_version must be >= 1", ErrInvalidManifest)
	}
	if m.ProtocolVersion > MaxProtocolVersion {
		return nil, fmt.Errorf("%w: plugin requires protocol version %d, this llm-proxy supports up to %d",
			ErrUnsupportedProtocol, m.ProtocolVersion, MaxProtocolVersion)
	}

	return &m, nil
}
