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
	"encoding/json"
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

// ErrToolNotFound is returned by ToolRunner.RunTool when the requested tool
// name is not registered in the backend's manifest.
var ErrToolNotFound = errors.New("tool not found")

// Model describes a single model exposed by a backend.
//
// Deprecated: Use ModelInfo instead. Will be removed in a future version.
type Model struct {
	// ID is the canonical model identifier as the backend expects it
	// (i.e. without any routing prefix).
	ID string `json:"id"`
	// OwnedBy is a free-form string included in OpenAI's /v1/models output.
	OwnedBy string `json:"owned_by"`
}

// TokenLimits describes the token capacity constraints for a model.
// All fields are optional; a zero value indicates the limit is unknown.
type TokenLimits struct {
	// MaxInputTokens is the maximum number of tokens accepted in the input.
	MaxInputTokens int `json:"max_input_tokens,omitempty"`
	// MaxOutputTokens is the maximum number of tokens the model will generate.
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`
	// MaxContextTokens is the total context window size (input + output).
	MaxContextTokens int `json:"max_context_tokens,omitempty"`
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
	// ReasoningEffortLevels lists supported reasoning effort levels for
	// models with reasoning capability (e.g. ["low", "medium", "high"]).
	// Only populated for models that include "reasoning" in Capabilities.
	ReasoningEffortLevels []string `json:"reasoning_effort_levels,omitempty"`
	// TokenLimits describes the model's token capacity constraints.
	// Nil if limits are unknown or not reported by the upstream.
	TokenLimits *TokenLimits `json:"token_limits,omitempty"`
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
// Deprecated: Backends should now implement Models() to return []ModelInfo directly.
// This interface exists for backward compatibility and will be removed in a future version.
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

// Reasoning holds optional reasoning parameters for /v1/responses requests.
type Reasoning struct {
	// Effort specifies the reasoning effort level. The proxy validates against
	// OpenAI's published set ("none", "minimal", "low", "medium", "high",
	// "xhigh", "max") before
	// dispatching to a backend.
	Effort string
}

// ResponsesRequest is the decoded /v1/responses request.
//
// Input carries the original JSON value of the `input` field, byte-exact from
// the client. Per OpenAI's Responses API it is either a JSON string or a JSON
// array of input items; the server validates the shape before populating this
// field. Backends that simply forward Body upstream can ignore Input.
type ResponsesRequest struct {
	Model     string
	Stream    bool
	Input     json.RawMessage
	Reasoning *Reasoning // Optional reasoning parameters
	Body      []byte
}

// Response is what a backend hands back to the server. Headers are copied to
// the downstream HTTP response and Body is streamed verbatim, so SSE works
// naturally for streaming requests.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       io.ReadCloser
}

// Budget describes a backend's current spend and remaining allowance.
//
// Field semantics:
//   - Currency is a short, lowercase tag describing the unit (e.g.
//     "bobcoins", "usd"). Empty when the upstream does not report one.
//   - MaxBudget is the configured ceiling. A value of 0 means "unknown",
//     and Unlimited reports whether the upstream explicitly marked the
//     account as having no ceiling (which providers should report as
//     MaxBudget=0, Unlimited=true).
//   - Spend is the cumulative amount consumed in the same unit as
//     MaxBudget.
//   - Remaining is a convenience field. When MaxBudget is 0 (unknown or
//     unlimited), backends should set Remaining to 0 and rely on the
//     caller inspecting Unlimited.
//   - Extras carries provider-specific supplemental fields (e.g.
//     instance_id, team_id, team_name) so that surfaces like
//     /v1/usage/budget can include them without expanding this struct.
type Budget struct {
	Currency  string            `json:"currency,omitempty"`
	MaxBudget float64           `json:"max_budget"`
	Spend     float64           `json:"spend"`
	Remaining float64           `json:"remaining"`
	Unlimited bool              `json:"unlimited,omitempty"`
	Extras    map[string]string `json:"extras,omitempty"`
}

// ToolRunner is an optional capability for backends (typically sidecar
// plugins) that can execute named tooling subcommands. The backend runs the
// tool out-of-process and returns its stdout as raw bytes.
//
// Implementations must return ErrToolNotFound when the requested tool name
// is not registered by the backend. Any non-zero exit or execution error
// should be returned as a plain error.
type ToolRunner interface {
	RunTool(ctx context.Context, toolName string) ([]byte, error)
}

// BudgetReporter is an optional capability for backends that expose a
// per-credential spend / quota figure (e.g. IBM Bob's "Bobcoins"). Backends
// that cannot report a budget should simply not implement it; callers must
// be prepared for the type assertion to fail.
//
// Implementations should return backend.ErrUnsupported when the configured
// credential shape does not expose budget info (for example, the legacy
// IBM Bob API key talks to a backend that does not surface /admin/v1/profile).
type BudgetReporter interface {
	Budget(ctx context.Context) (Budget, error)
}

// Reloadable is implemented by backends that can hot-reload their in-memory
// credential cache from disk on SIGHUP without restarting the HTTP listener.
// The Reload method must be safe for concurrent use.
type Reloadable interface {
	Reload()
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
	// aggregates results across backends to serve /v1/models and /v1/model/info.
	// Returns ModelInfo with full metadata including capabilities and token limits.
	Models(ctx context.Context) ([]ModelInfo, error)

	// Chat handles a chat-completion request. Streaming requests are
	// indicated by ChatRequest.Stream; the returned Body should be
	// streamed to the client without buffering.
	Chat(ctx context.Context, req ChatRequest) (*Response, error)

	// Complete handles a legacy text completion. May return ErrUnsupported.
	Complete(ctx context.Context, req CompletionRequest) (*Response, error)

	// Embed handles an embedding request. May return ErrUnsupported.
	Embed(ctx context.Context, req EmbeddingRequest) (*Response, error)

	// Responses handles a unified response request with optional reasoning.
	// May return ErrUnsupported if the backend does not support reasoning models.
	Responses(ctx context.Context, req ResponsesRequest) (*Response, error)
}
