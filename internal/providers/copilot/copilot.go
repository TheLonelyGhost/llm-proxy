// Package copilot implements the GitHub Copilot backend.
//
// Authentication is a two-step process:
//
//  1. The proxy obtains a long-lived GitHub OAuth token via the device-code
//     flow (interactive, one-time) and stores it under the user's config
//     directory with mode 0600.
//
//  2. On each API call the proxy exchanges that GitHub token for a
//     short-lived Copilot API token, which it caches under the user's cache
//     directory and reuses until shortly before its expiry.
//
// The default OAuth client id is the publicly known id used by the official
// Copilot editor extensions. It can be overridden via configuration.
package copilot

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
	"github.com/thelonelyghost/llm-proxy/internal/config"
)

// Type is the value users put in `backend "x" { type = ... }` to select this
// provider.
const Type = "github-copilot"

// Defaults used when fields are omitted from configuration.
const (
	DefaultAPIBase             = "https://api.githubcopilot.com"
	DefaultGitHubAPIBase       = "https://api.github.com"
	DefaultGitHubLoginBase     = "https://github.com"
	DefaultOAuthClientID       = "Iv1.b507a08c87ecfe98"
	DefaultEditorVersion       = "vscode/1.95.0"
	DefaultEditorPluginVersion = "copilot-chat/0.22.0"
	DefaultUserAgent           = "GitHubCopilotChat/0.22.0"
	DefaultIntegrationID       = "vscode-chat"
	DefaultRequestTimeout      = 60 * time.Second
)

// Settings is the provider-specific HCL block contents.
type Settings struct {
	APIBase             string `hcl:"api_base,optional"`
	GitHubAPIBase       string `hcl:"github_api_base,optional"`
	GitHubLoginBase     string `hcl:"github_login_base,optional"`
	OAuthClientID       string `hcl:"oauth_client_id,optional"`
	EditorVersion       string `hcl:"editor_version,optional"`
	EditorPluginVersion string `hcl:"editor_plugin_version,optional"`
	UserAgent           string `hcl:"user_agent,optional"`
	IntegrationID       string `hcl:"integration_id,optional"`
	RequestTimeout      string `hcl:"request_timeout,optional"`
}

// applyDefaults fills in unset Settings fields with safe defaults.
func (s *Settings) applyDefaults() {
	if s.APIBase == "" {
		s.APIBase = DefaultAPIBase
	}
	if s.GitHubAPIBase == "" {
		s.GitHubAPIBase = DefaultGitHubAPIBase
	}
	if s.GitHubLoginBase == "" {
		s.GitHubLoginBase = DefaultGitHubLoginBase
	}
	if s.OAuthClientID == "" {
		s.OAuthClientID = DefaultOAuthClientID
	}
	if s.EditorVersion == "" {
		s.EditorVersion = DefaultEditorVersion
	}
	if s.EditorPluginVersion == "" {
		s.EditorPluginVersion = DefaultEditorPluginVersion
	}
	if s.UserAgent == "" {
		s.UserAgent = DefaultUserAgent
	}
	if s.IntegrationID == "" {
		s.IntegrationID = DefaultIntegrationID
	}
}

// resolveTimeout returns either the parsed RequestTimeout or DefaultRequestTimeout.
func (s *Settings) resolveTimeout() (time.Duration, error) {
	if s.RequestTimeout == "" {
		return DefaultRequestTimeout, nil
	}
	d, err := time.ParseDuration(s.RequestTimeout)
	if err != nil {
		return 0, fmt.Errorf("parse request_timeout %q: %w", s.RequestTimeout, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("request_timeout must be positive, got %s", d)
	}
	return d, nil
}

// Provider is the Backend implementation for GitHub Copilot.
type Provider struct {
	name     string
	settings Settings
	models   []string
	auth     *Authenticator
	http     *http.Client
}

// Options are inputs to New that come from outside the HCL block (paths,
// allow-listed models from the BackendBlock label/Models field, etc.).
type Options struct {
	// Name is the backend label (used as routing prefix).
	Name string
	// Models is the optional allow-list parsed from the BackendBlock.
	Models []string
	// TokenStore overrides the on-disk token store; nil uses the
	// platform-default paths.
	TokenStore TokenStore
	// HTTPClient overrides the HTTP client used for upstream calls; nil
	// uses a sensible default with the configured request timeout.
	HTTPClient *http.Client
	// IntervalUnit overrides the unit of duration used when interpreting
	// device-code poll intervals. Intended for tests; zero leaves the
	// default of time.Second in place.
	IntervalUnit time.Duration
}

// New constructs a Provider from a BackendBlock and Options.
func New(block config.BackendBlock, opts Options) (*Provider, error) {
	if block.Type != Type {
		return nil, fmt.Errorf("copilot.New: block type %q != %q", block.Type, Type)
	}
	var s Settings
	if err := config.DecodeBackend(block, &s); err != nil {
		return nil, err
	}
	s.applyDefaults()

	timeout, err := s.resolveTimeout()
	if err != nil {
		return nil, err
	}

	store := opts.TokenStore
	if store == nil {
		store, err = NewFileTokenStore()
		if err != nil {
			return nil, fmt.Errorf("init token store: %w", err)
		}
	}

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	auth := NewAuthenticator(AuthConfig{
		ClientID:        s.OAuthClientID,
		GitHubAPIBase:   s.GitHubAPIBase,
		GitHubLoginBase: s.GitHubLoginBase,
		UserAgent:       s.UserAgent,
		EditorVersion:   s.EditorVersion,
		Store:           store,
		HTTPClient:      client,
		IntervalUnit:    opts.IntervalUnit,
	})

	name := opts.Name
	if name == "" {
		name = block.Name
	}
	models := opts.Models
	if len(models) == 0 {
		models = block.Models
	}

	return &Provider{
		name:     name,
		settings: s,
		models:   models,
		auth:     auth,
		http:     client,
	}, nil
}

// Name returns the routing prefix for this backend.
func (p *Provider) Name() string { return p.name }

// Auth exposes the underlying Authenticator for the `login` and `logout`
// CLI subcommands.
func (p *Provider) Auth() *Authenticator { return p.auth }

// Models returns the configured allow-list. If empty, the proxy refuses to
// list models for this backend; clients may still address it explicitly.
//
// We intentionally do not query Copilot's /models endpoint here because the
// upstream list churns and depends on the user's entitlement; operators
// should pin the models they trust in HCL.
func (p *Provider) Models(_ context.Context) ([]backend.Model, error) {
	out := make([]backend.Model, 0, len(p.models))
	for _, m := range p.models {
		out = append(out, backend.Model{ID: m, OwnedBy: p.name})
	}
	return out, nil
}

// Chat forwards a chat-completions request to Copilot.
func (p *Provider) Chat(ctx context.Context, req backend.ChatRequest) (*backend.Response, error) {
	if err := p.allowed(req.Model); err != nil {
		return nil, err
	}
	return p.forward(ctx, "POST", "/chat/completions", req.Body, req.Stream)
}

// Complete forwards a legacy completions request to Copilot.
func (p *Provider) Complete(ctx context.Context, req backend.CompletionRequest) (*backend.Response, error) {
	if err := p.allowed(req.Model); err != nil {
		return nil, err
	}
	return p.forward(ctx, "POST", "/completions", req.Body, req.Stream)
}

// Embed forwards an embeddings request to Copilot.
func (p *Provider) Embed(ctx context.Context, req backend.EmbeddingRequest) (*backend.Response, error) {
	if err := p.allowed(req.Model); err != nil {
		return nil, err
	}
	return p.forward(ctx, "POST", "/embeddings", req.Body, false)
}

// allowed enforces the configured model allow-list. An empty allow-list
// permits any model.
func (p *Provider) allowed(model string) error {
	if len(p.models) == 0 {
		return nil
	}
	for _, m := range p.models {
		if m == model {
			return nil
		}
	}
	return fmt.Errorf("%w: %q not in backend %q allow-list", backend.ErrUnknownModel, model, p.name)
}
