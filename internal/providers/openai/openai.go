// Package openai implements the OpenAI-compatible backend provider.
//
// This provider supports any OpenAI-compatible API endpoint, including
// the official OpenAI API as well as third-party implementations that
// expose the same interface. Configuration requires either an api_key
// (for authenticated endpoints like OpenAI) or a custom base_url (for
// endpoints that may not require authentication).
package openai

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
	"github.com/thelonelyghost/llm-proxy/internal/config"
	"github.com/thelonelyghost/llm-proxy/internal/httplog"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// Type is the HCL `type` value selecting this provider.
const Type = "openai"

// Default configuration values.
const (
	DefaultBaseURL        = "https://api.openai.com/v1"
	DefaultRequestTimeout = 60 * time.Second
)

// Settings is the HCL-decodable provider configuration.
type Settings struct {
	BaseURL        string `hcl:"base_url,optional"`
	APIKey         string `hcl:"api_key,optional"`
	RequestTimeout string `hcl:"request_timeout,optional"`
}

func (s *Settings) applyDefaults() {
	if s.BaseURL == "" {
		s.BaseURL = DefaultBaseURL
	}
}

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

// Provider is the backend.Backend implementation for OpenAI-compatible APIs.
type Provider struct {
	name     string
	settings Settings
	models   []string
	client   *http.Client
	logger   *slog.Logger
	tracer   trace.Tracer
}

// Options carries non-HCL inputs.
type Options struct {
	Name       string
	Models     []string
	HTTPClient *http.Client
	Logger     *slog.Logger // nil defaults to slog.Default()
	LogBodies  bool
	Tracer     trace.Tracer
}

// New constructs a Provider.
func New(block config.BackendBlock, opts Options) (*Provider, error) {
	if block.Type != Type {
		return nil, fmt.Errorf("openai.New: block type %q != %q", block.Type, Type)
	}
	var s Settings
	if err := config.DecodeBackend(block, &s); err != nil {
		return nil, err
	}
	hasExplicitBaseURL := s.BaseURL != ""
	s.applyDefaults()

	s.BaseURL = strings.TrimSuffix(s.BaseURL, "/v1")

	if s.APIKey == "" && !hasExplicitBaseURL {
		return nil, fmt.Errorf("openai backend %q: api_key is required unless base_url is specified", block.Name)
	}

	timeout, err := s.resolveTimeout()
	if err != nil {
		return nil, err
	}

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if logger.Enabled(context.Background(), slog.LevelDebug) {
		httplog.WrapClient(client, logger, opts.LogBodies)
	}

	tracer := opts.Tracer
	if tracer == nil {
		tracer = tracenoop.NewTracerProvider().Tracer(Type)
	}

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
		client:   client,
		logger:   logger,
		tracer:   tracer,
	}, nil
}

// Name returns the routing prefix.
func (p *Provider) Name() string { return p.name }

// Models returns the configured allow-list with full metadata.
func (p *Provider) Models(ctx context.Context) ([]backend.ModelInfo, error) {
	ctx, span := p.tracer.Start(ctx, "openai.models")
	defer span.End()
	// If no models configured, return empty list (allow-list mode)
	if len(p.models) == 0 {
		return nil, nil
	}

	// Get full model metadata from upstream
	allModels, err := p.ListUpstreamModels(ctx)
	if err != nil {
		span.RecordError(err)
		span.AddEvent("upstream model listing failed; using configured allow-list")
		p.logger.Warn("openai model listing failed; using allow-list fallback", "backend", p.name, "err", err)
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

// Chat forwards a chat-completions request to the OpenAI-compatible API.
func (p *Provider) Chat(ctx context.Context, req backend.ChatRequest) (*backend.Response, error) {
	if err := p.allowed(req.Model); err != nil {
		return nil, err
	}
	return p.forward(ctx, "/v1/chat/completions", req.Body, req.Stream)
}

// Complete forwards a legacy completions request.
func (p *Provider) Complete(ctx context.Context, req backend.CompletionRequest) (*backend.Response, error) {
	if err := p.allowed(req.Model); err != nil {
		return nil, err
	}
	return p.forward(ctx, "/v1/completions", req.Body, req.Stream)
}

// Embed forwards an embeddings request.
func (p *Provider) Embed(ctx context.Context, req backend.EmbeddingRequest) (*backend.Response, error) {
	if err := p.allowed(req.Model); err != nil {
		return nil, err
	}
	return p.forward(ctx, "/v1/embeddings", req.Body, false)
}

// Responses forwards a unified response request with reasoning support.
func (p *Provider) Responses(ctx context.Context, req backend.ResponsesRequest) (*backend.Response, error) {
	if err := p.allowed(req.Model); err != nil {
		return nil, err
	}
	return p.forward(ctx, "/v1/responses", req.Body, req.Stream)
}
