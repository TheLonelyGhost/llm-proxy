// Package ibmbob implements the IBM Bob backend.
//
// The wire protocol mirrors the official `bob` CLI: API-key shape selects the
// upstream host and authentication scheme, and HMAC-SHA256 request signing is
// applied for the modern backend. Instance and team ids are auto-derived
// from /admin/v1/profile the first time the proxy needs them, then cached
// for the life of the process.
//
// Configuration lives in the BackendBlock HCL body; the only required field
// is api_key. The HMAC build-time secret used by the modern backend is not
// embedded in this package - operators must supply it via the hmac_secret
// HCL attribute (typically wrapped in env("BOB_HMAC_SECRET")). See
// docs/providers/ibm-bob.md for extraction instructions.
package ibmbob

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
	"github.com/thelonelyghost/llm-proxy/internal/config"
)

// Type is the HCL `type` value selecting this provider.
const Type = "ibm-bob"

// Default upstream endpoints. These mirror the constants in the reference
// implementation (TheLonelyGhost/litellm-ibm-bob).
const (
	DefaultBackendURL    = "https://api.us-east.bob.ibm.com"
	DefaultOldBackendURL = "https://prod.ibm-bob-staging.cloud.ibm.com"

	chatPathAuthn  = "/inference/v1/chat/completions"
	chatPathLegacy = "/chat/completions"

	completionsPath = "/v1/completions" // bob exposes the same legacy completions surface
	embeddingsPath  = "/embeddings"
	profilePath     = "/admin/v1/profile"

	DefaultUserAgent     = "bobshell/1.0.4"
	DefaultTimeout       = 300 * time.Second
	DefaultMaxRetries    = 6
	DefaultRetryDelay    = time.Second
	DefaultPlatformTitle = "ibm-bob-llm-proxy"
	defaultMetadataMode  = "default"
	defaultIdentityTTL   = 30 * time.Minute
)

// Settings is the HCL-decodable provider configuration.
type Settings struct {
	APIKey         string `hcl:"api_key,optional"`
	HMACSecret     string `hcl:"hmac_secret,optional"`
	APIBase        string `hcl:"api_base,optional"`
	InstanceID     string `hcl:"instance_id,optional"`
	TeamID         string `hcl:"team_id,optional"`
	UserAgent      string `hcl:"user_agent,optional"`
	RequestTimeout string `hcl:"request_timeout,optional"`
	MaxRetries     int    `hcl:"max_retries,optional"`
	RetryDelay     string `hcl:"retry_delay,optional"`
	PlatformTitle  string `hcl:"platform_title,optional"`
}

// applyDefaults fills in unset fields with safe defaults.
func (s *Settings) applyDefaults() {
	if s.UserAgent == "" {
		s.UserAgent = DefaultUserAgent
	}
	if s.MaxRetries == 0 {
		s.MaxRetries = DefaultMaxRetries
	}
	if s.PlatformTitle == "" {
		s.PlatformTitle = DefaultPlatformTitle
	}
}

func (s *Settings) resolveTimeout() (time.Duration, error) {
	if s.RequestTimeout == "" {
		return DefaultTimeout, nil
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

func (s *Settings) resolveRetryDelay() (time.Duration, error) {
	if s.RetryDelay == "" {
		return DefaultRetryDelay, nil
	}
	d, err := time.ParseDuration(s.RetryDelay)
	if err != nil {
		return 0, fmt.Errorf("parse retry_delay %q: %w", s.RetryDelay, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("retry_delay must be non-negative, got %s", d)
	}
	return d, nil
}

// Provider is the backend.Backend implementation.
type Provider struct {
	name     string
	settings Settings
	models   []string
	client   *http.Client

	keyKind      KeyKind
	baseURL      string
	authnBackend bool
	hmacSecret   []byte
	maxRetries   int
	retryDelay   time.Duration

	identityMu      sync.Mutex
	identity        Identity
	identityFetched time.Time
	identityTTL     time.Duration
	// now is overridable in tests for deterministic timestamps.
	now func() time.Time
}

// Options carries non-HCL inputs (chiefly used by the registry and by tests).
type Options struct {
	Name        string
	Models      []string
	HTTPClient  *http.Client
	IdentityTTL time.Duration
	Now         func() time.Time
}

// New constructs a Provider.
func New(block config.BackendBlock, opts Options) (*Provider, error) {
	if block.Type != Type {
		return nil, fmt.Errorf("ibmbob.New: block type %q != %q", block.Type, Type)
	}
	var s Settings
	if err := config.DecodeBackend(block, &s); err != nil {
		return nil, err
	}
	if s.APIKey == "" {
		return nil, fmt.Errorf("ibmbob backend %q: api_key is required", block.Name)
	}
	s.applyDefaults()
	timeout, err := s.resolveTimeout()
	if err != nil {
		return nil, err
	}
	retryDelay, err := s.resolveRetryDelay()
	if err != nil {
		return nil, err
	}
	kind, err := ClassifyKey(s.APIKey)
	if err != nil {
		return nil, fmt.Errorf("ibmbob backend %q: %w", block.Name, err)
	}
	baseURL := BaseURLFor(s.APIKey, kind, s.APIBase)
	authn := IsAuthnBackend(baseURL, kind)
	if authn && s.HMACSecret == "" {
		return nil, fmt.Errorf("ibmbob backend %q: hmac_secret is required for the %s host", block.Name, baseURL)
	}

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	name := opts.Name
	if name == "" {
		name = block.Name
	}
	models := opts.Models
	if len(models) == 0 {
		models = block.Models
	}
	ttl := opts.IdentityTTL
	if ttl == 0 {
		ttl = defaultIdentityTTL
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	return &Provider{
		name:         name,
		settings:     s,
		models:       models,
		client:       client,
		keyKind:      kind,
		baseURL:      baseURL,
		authnBackend: authn,
		hmacSecret:   []byte(s.HMACSecret),
		maxRetries:   s.MaxRetries,
		retryDelay:   retryDelay,
		identity:     Identity{InstanceID: s.InstanceID, TeamID: s.TeamID},
		identityTTL:  ttl,
		now:          now,
	}, nil
}

// Name returns the routing prefix.
func (p *Provider) Name() string { return p.name }

// KeyKind reports how the configured API key was classified.
func (p *Provider) KeyKind() KeyKind { return p.keyKind }

// BaseURL returns the resolved upstream base URL.
func (p *Provider) BaseURL() string { return p.baseURL }

// Models returns the configured allow-list with full metadata.
func (p *Provider) Models(ctx context.Context) ([]backend.ModelInfo, error) {
	// If no models configured, return empty list (allow-list mode)
	if len(p.models) == 0 {
		return nil, nil
	}

	// Get full model metadata from upstream
	allModels, err := p.ListUpstreamModels(ctx)
	if err != nil {
		// Fallback to minimal ModelInfo if upstream query fails
		out := make([]backend.ModelInfo, 0, len(p.models))
		for _, m := range p.models {
			out = append(out, backend.ModelInfo{ID: m, Vendor: p.name})
		}
		return out, nil
	}

	// Filter to configured allow-list
	modelSet := make(map[string]bool, len(p.models))
	for _, m := range p.models {
		modelSet[m] = true
	}

	out := make([]backend.ModelInfo, 0, len(p.models))
	for _, info := range allModels {
		if modelSet[info.ID] {
			out = append(out, info)
		}
	}
	return out, nil
}

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

// Chat forwards a chat-completions request to Bob.
func (p *Provider) Chat(ctx context.Context, req backend.ChatRequest) (*backend.Response, error) {
	if err := p.allowed(req.Model); err != nil {
		return nil, err
	}
	if err := p.ensureIdentity(ctx); err != nil {
		return nil, err
	}
	path := chatPathLegacy
	if p.authnBackend {
		path = chatPathAuthn
	}
	return p.forward(ctx, http.MethodPost, path, req.Body, req.Stream)
}

// Complete forwards a legacy completions request.
//
// Bob's wire protocol is OpenAI-compatible; the modern backend does not
// document a `/v1/completions` route, so this is best-effort.
func (p *Provider) Complete(ctx context.Context, req backend.CompletionRequest) (*backend.Response, error) {
	if err := p.allowed(req.Model); err != nil {
		return nil, err
	}
	if err := p.ensureIdentity(ctx); err != nil {
		return nil, err
	}
	if p.keyKind == KeyKindLegacy {
		return p.forward(ctx, http.MethodPost, completionsPath, req.Body, req.Stream)
	}
	return nil, fmt.Errorf("%w: %s does not expose legacy /completions on the authn backend",
		backend.ErrUnsupported, p.name)
}

// Embed forwards an embeddings request.
func (p *Provider) Embed(ctx context.Context, req backend.EmbeddingRequest) (*backend.Response, error) {
	if err := p.allowed(req.Model); err != nil {
		return nil, err
	}
	if err := p.ensureIdentity(ctx); err != nil {
		return nil, err
	}
	return p.forward(ctx, http.MethodPost, embeddingsPath, req.Body, false)
}

// Responses returns ErrUnsupported as IBM Bob does not currently support reasoning models.
func (p *Provider) Responses(ctx context.Context, req backend.ResponsesRequest) (*backend.Response, error) {
	return nil, fmt.Errorf("%w: ibm-bob does not support /responses endpoint", backend.ErrUnsupported)
}

// Identity holds the resolved (instance, team) pair.
type Identity struct {
	InstanceID string
	TeamID     string
}

// Resolved reports whether both halves are populated.
func (i Identity) Resolved() bool { return i.InstanceID != "" && i.TeamID != "" }

// ensureIdentity populates p.identity if it is unresolved (or stale). It is a
// no-op for legacy keys (which talk to the staging backend and do not expose
// /admin/v1/profile) and when the operator pinned both ids in HCL.
func (p *Provider) ensureIdentity(ctx context.Context) error {
	if p.keyKind == KeyKindLegacy {
		return nil
	}
	p.identityMu.Lock()
	defer p.identityMu.Unlock()
	if p.identity.Resolved() && p.now().Sub(p.identityFetched) < p.identityTTL {
		return nil
	}
	// If the operator pinned only one half, keep the override and skip the
	// network call.
	if p.settings.InstanceID != "" && p.settings.TeamID != "" {
		p.identity = Identity{InstanceID: p.settings.InstanceID, TeamID: p.settings.TeamID}
		p.identityFetched = p.now()
		return nil
	}
	resolved, err := p.fetchIdentity(ctx)
	if err != nil {
		return err
	}
	// Apply operator overrides on top of the fetched identity.
	if p.settings.InstanceID != "" {
		resolved.InstanceID = p.settings.InstanceID
	}
	if p.settings.TeamID != "" {
		resolved.TeamID = p.settings.TeamID
	}
	p.identity = resolved
	p.identityFetched = p.now()
	return nil
}

// currentIdentity is the snapshot used when building outbound headers.
func (p *Provider) currentIdentity() Identity {
	p.identityMu.Lock()
	defer p.identityMu.Unlock()
	return p.identity
}

// errProfileFetch is returned when /admin/v1/profile cannot be loaded.
var errProfileFetch = errors.New("profile fetch failed")
