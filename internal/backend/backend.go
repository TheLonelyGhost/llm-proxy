// Package backend defines the abstraction every LLM provider must implement
// to participate in the proxy, plus a small registry for plugging providers
// in at startup.
//
// The interfaces are deliberately narrow and mirror the subset of the OpenAI
// HTTP API the proxy speaks: chat completions (with streaming), legacy text
// completions, embeddings, and model listing. A backend may return
// ErrUnsupported from any optional capability; the server will translate that
// into HTTP 501 Not Implemented for clients.
package backend

import (
	"context"
	"errors"
	"io"
	"net/http"
)

// ErrUnsupported is returned by a Backend when it does not implement a
// particular OpenAI-compatible capability (e.g., embeddings).
var ErrUnsupported = errors.New("capability not supported by backend")

// ErrUnknownModel is returned when a backend is asked to serve a model id
// that it does not recognise.
var ErrUnknownModel = errors.New("unknown model")

// Model describes a single model exposed by a backend.
type Model struct {
	// ID is the canonical model identifier as the backend expects it
	// (i.e. without any routing prefix).
	ID string `json:"id"`
	// OwnedBy is a free-form string included in OpenAI's /v1/models output.
	OwnedBy string `json:"owned_by"`
}

// ModelInfo describes a single upstream model with supplemental metadata as
// reported by the provider's own model-listing endpoint. It is intended for
// human-facing tooling (the `providers list-models` CLI) rather than the
// /v1/models proxy surface, which deliberately uses the operator-curated
// allow-list.
//
// All fields except ID are best-effort; backends populate whatever subset of
// metadata they can extract from the upstream payload. Extras carries
// provider-specific key/value pairs in deterministic (sorted) key order so
// the CLI can render them stably.
type ModelInfo struct {
	// ID is the canonical model identifier as the backend expects it.
	ID string `json:"id"`
	// Name is a human-friendly display name when the upstream provides one.
	Name string `json:"name,omitempty"`
	// Vendor identifies the model's vendor (e.g. "Anthropic", "OpenAI").
	Vendor string `json:"vendor,omitempty"`
	// Version is the upstream-reported model version when present.
	Version string `json:"version,omitempty"`
	// Capabilities lists feature tags reported by the upstream (e.g.
	// "chat", "embeddings", "tool_calls"). Order is upstream-defined.
	Capabilities []string `json:"capabilities,omitempty"`
	// Deprecated is true when the upstream marks the model as
	// deprecated or sunset.
	Deprecated bool `json:"deprecated,omitempty"`
	// Extras holds provider-specific supplemental fields. Keys are
	// stable, lowercase, snake_case strings.
	Extras map[string]string `json:"extras,omitempty"`
}

// ModelLister is an optional capability for backends that can enumerate the
// models the configured credentials are entitled to use, with supplemental
// metadata. Backends that cannot (or do not) talk to an upstream catalogue
// should simply not implement it.
//
// Implementations must filter out models that the upstream has explicitly
// disabled (for example via policy) so the returned slice represents only
// models the caller could realistically request.
type ModelLister interface {
	ListUpstreamModels(ctx context.Context) ([]ModelInfo, error)
}

// ChatRequest is the decoded, prefix-stripped chat-completions request.
// Body holds the original JSON bytes (with the model field rewritten to the
// canonical id), allowing the backend to forward upstream verbatim if it
// chooses.
type ChatRequest struct {
	Model  string
	Stream bool
	Body   []byte
}

// CompletionRequest mirrors ChatRequest for the legacy /v1/completions API.
type CompletionRequest struct {
	Model  string
	Stream bool
	Body   []byte
}

// EmbeddingRequest mirrors ChatRequest for /v1/embeddings.
type EmbeddingRequest struct {
	Model string
	Body  []byte
}

// Response is what a backend hands back to the server. Headers are copied to
// the downstream HTTP response and Body is streamed verbatim, so SSE works
// naturally for streaming requests.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       io.ReadCloser
}

// Backend is the interface every provider must satisfy.
//
// Implementations must be safe for concurrent use.
type Backend interface {
	// Name returns the routing prefix for this backend (e.g. "copilot").
	// It must be stable, lowercase, and match the prefix clients use
	// before the slash in model identifiers.
	Name() string

	// Models lists the models this backend will accept. The proxy
	// aggregates results across backends to serve /v1/models.
	Models(ctx context.Context) ([]Model, error)

	// Chat handles a chat-completion request. Streaming requests are
	// indicated by ChatRequest.Stream; the returned Body should be
	// streamed to the client without buffering.
	Chat(ctx context.Context, req ChatRequest) (*Response, error)

	// Complete handles a legacy text completion. May return ErrUnsupported.
	Complete(ctx context.Context, req CompletionRequest) (*Response, error)

	// Embed handles an embedding request. May return ErrUnsupported.
	Embed(ctx context.Context, req EmbeddingRequest) (*Response, error)
}
