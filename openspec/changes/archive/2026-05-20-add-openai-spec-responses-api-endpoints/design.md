## Context

The proxy currently acts as a pass-through for backend responses. Backends (GitHub Copilot, IBM Bob, OpenAI) may return different response structures, error formats, and streaming conventions. Clients expecting strict OpenAI API compliance can fail when receiving non-conformant responses.

Current flow:
1. Client sends request to proxy
2. Proxy routes to backend based on model prefix
3. Backend returns `backend.Response` (status code, headers, body stream)
4. Server writes response directly to client via `writeUpstreamResponse`

The transformation layer must sit between step 3 and 4 without breaking streaming performance or altering backend implementations.

## Goals / Non-Goals

**Goals:**
- Transform backend responses to match OpenAI API specification exactly
- Preserve streaming performance (no buffering of SSE streams)
- Support all current endpoints: `/v1/chat/completions`, `/v1/completions`, `/v1/embeddings`
- Add new `/v1/responses` endpoint for unified response objects with reasoning effort support
- Standardize error responses across all backends
- Maintain backward compatibility with existing backend implementations

**Non-Goals:**
- Modifying backend implementations
- Validating request payloads (already handled by server)
- Supporting non-OpenAI API endpoints
- Response caching or modification for performance

## Decisions

### Decision 1: Transform at server layer, not backend interface

**Rationale:** Adding transformation logic to `writeUpstreamResponse` in `internal/server/handlers.go` keeps backends simple and avoids duplicating transformation code across providers.

**Alternatives considered:**
- Require backends to implement OpenAI spec: High implementation burden, duplicates logic across 3+ providers
- Middleware wrapper around backends: Adds complexity, harder to debug streaming issues

**Trade-off:** Server layer becomes slightly more complex, but centralized transformation logic is easier to test and maintain.

### Decision 2: Streaming transformation via SSE parser/rewriter

**Rationale:** Streaming responses must be transformed incrementally without buffering the entire response. Parse SSE events on-the-fly and rewrite data payloads to match OpenAI's `data: {...}\n\n` format.

**Implementation:**
- Create `internal/transform` package with stream transformer
- Use `bufio.Scanner` with SSE line delimiter
- Transform each `data:` line independently
- Pass through non-data lines (comments, keep-alives)

**Alternatives considered:**
- Buffer entire response then transform: Defeats streaming, unacceptable memory usage
- No streaming transformation: Would break clients expecting OpenAI SSE format

### Decision 3: Separate transformers per endpoint type

**Rationale:** Chat, completion, embedding, and responses have different schemas. Create separate transformer functions:
- `TransformChatResponse(statusCode int, body io.ReadCloser, stream bool) (*Response, error)`
- `TransformCompletionResponse(statusCode int, body io.ReadCloser, stream bool) (*Response, error)`
- `TransformEmbeddingResponse(statusCode int, body io.ReadCloser) (*Response, error)`
- `TransformResponsesResponse(statusCode int, body io.ReadCloser, stream bool) (*Response, error)`

Each parses the expected schema and outputs conformant JSON.

**Alternatives considered:**
- Single generic transformer: Would need complex type detection, harder to maintain
- Transform at JSON schema level: Overly complex, doesn't handle streaming well

### Decision 4: Error response transformation at API boundary

**Rationale:** Current `writeAPIError` function already writes OpenAI-format errors for proxy-generated errors. Extend this to transform backend error responses:
- Parse backend error response body
- Extract message, type, code if present
- Rewrite to OpenAI format: `{"error": {"message": "...", "type": "...", "code": null}}`
- Default to `type: "upstream_error"` for unparseable errors

### Decision 5: No validation of backend response schemas

**Rationale:** Transformation is best-effort. If backend returns malformed JSON, pass through the original error rather than failing silently.

**Trade-off:** May expose non-conformant responses if backend has bugs, but prevents hiding real issues.

### Decision 6: Add `/v1/responses` endpoint with backend interface extension

**Rationale:** The `/v1/responses` endpoint provides a unified API for response objects that may include reasoning effort. This requires extending the `backend.Backend` interface with a new method. Uses `input` field instead of `messages` for simpler text-based requests, and nests effort within a `reasoning` object per OpenAI spec.

**Implementation:**
- Add `Responses(ctx context.Context, req ResponsesRequest) (*Response, error)` to `backend.Backend` interface
- `ResponsesRequest` includes `Model`, `Stream`, `Input`, `Reasoning` (optional object with `Effort` field), and `Body` fields
- `Reasoning` struct contains `Effort` string field that accepts "low", "medium", or "high"
- Request parsing extracts `input` field (required) and optional `reasoning.effort` field
- Backends may return `ErrUnsupported` if they don't support reasoning capabilities
- Response transformer validates and normalizes reasoning output fields

**Alternatives considered:**
- Reuse Chat endpoint with special parameter: Breaks API semantics, confusing for clients
- Client-side routing to chat: Loses reasoning-specific features, no server-side validation
- Flat `reasoning_effort` parameter: Doesn't match OpenAI spec structure

**Trade-off:** Requires all backends to implement new method (can return `ErrUnsupported`), but provides clean separation for reasoning-enabled models and matches OpenAI API structure exactly.

### Decision 7: Model capability discovery via dual endpoints

**Rationale:** The OpenAI `/v1/models` specification does not include capability metadata, but clients (especially agentic frameworks) need to discover which features each model supports. Capabilities vary by both backend AND model (e.g., `openai/gpt-4o` supports vision but `copilot/gpt-4o` does not, even though both serve the same base model).

**Implementation:**
- `/v1/models` returns exact OpenAI spec format (no capabilities field)
- `/v1/model/info` (non-standard extension) returns rich metadata including capabilities, token limits, reasoning effort levels
- Change `Backend.Models()` signature from `[]Model` to `[]ModelInfo` (breaking change acceptable in early development)
- Backends derive capabilities from upstream provider responses via `ListUpstreamModels()`
- Copilot already parses `capabilities.supports` from upstream
- OpenAI and IBM Bob backends extended to extract available capability metadata
- `ModelInfo` extended with optional `ReasoningEffortLevels []string` field for models supporting reasoning
- `ModelInfo` extended with optional `TokenLimits` struct containing `MaxInputTokens`, `MaxOutputTokens`, `MaxContextTokens`

**Response format:**

```
GET /v1/models  (OpenAI-compliant)
{
  "object": "list",
  "data": [
    {"id": "copilot/gpt-4o", "object": "model", "created": 0, "owned_by": "copilot"}
  ]
}

GET /v1/model/info  (proxy extension)
{
  "object": "list",
  "data": [
    {
      "id": "copilot/gpt-4o",
      "name": "GPT-4o",
      "vendor": "github",
      "capabilities": ["chat", "tools", "streaming"],
      "token_limits": {
        "max_input_tokens": 128000,
        "max_output_tokens": 4096,
        "max_context_tokens": 128000
      },
      "deprecated": false
    },
    {
      "id": "openai/gpt-4o",
      "name": "GPT-4o",
      "vendor": "openai",
      "capabilities": ["chat", "tools", "vision", "streaming"],
      "token_limits": {
        "max_input_tokens": 128000,
        "max_output_tokens": 16384,
        "max_context_tokens": 128000
      },
      "deprecated": false
    },
    {
      "id": "openai/o1-preview",
      "name": "o1-preview",
      "vendor": "openai",
      "capabilities": ["chat", "reasoning"],
      "reasoning_effort_levels": ["low", "medium", "high"],
      "token_limits": {
        "max_input_tokens": 128000,
        "max_output_tokens": 32768,
        "max_context_tokens": 128000
      },
      "deprecated": false
    }
  ]
}
```

**Alternatives considered:**
- Extend `/v1/models` with capabilities field: Breaks OpenAI spec compliance
- Require clients to probe capabilities: Inefficient, requires upstream API calls
- Static capability mapping: Fragile, breaks when models/features change

**Trade-off:** Non-standard endpoint required for discovery, but maintains strict OpenAI compatibility on `/v1/models` while enabling graceful degradation in clients.

### Decision 8: Backend interface breaking change acceptable

**Rationale:** Proxy is in early development phase. Prefer OpenAI specification compliance over backend interface stability.

**Implementation:**
- Change `Models(ctx) ([]Model, error)` to `Models(ctx) ([]ModelInfo, error)`
- Remove simplified `Model` type, use rich `ModelInfo` everywhere
- Deprecate `ModelLister` interface (backends use `Models()` for both use cases)
- All three backends (Copilot, OpenAI, IBM Bob) already implement `ListUpstreamModels()`
- Migration: backends delegate `Models()` to `ListUpstreamModels()` implementation

**Benefits:**
- Single method returns both minimal data (for `/v1/models`) and rich data (for `/v1/model/info`)
- Capabilities derived from upstream providers, not static configuration
- Reduces interface surface area
- Enables model-specific capability checks at request time

## Risks / Trade-offs

**[Risk]** Backend response schema changes break transformation
→ **Mitigation:** Unit tests with fixtures from each backend; integration tests against live backends; fallback to pass-through on parse errors

**[Risk]** Streaming transformation introduces latency
→ **Mitigation:** Line-by-line transformation avoids buffering; benchmark with 1MB+ streamed responses

**[Risk]** Memory growth from SSE parsing
→ **Mitigation:** Use bounded buffers in `bufio.Scanner`, reject events >64KB

**[Trade-off]** Transformation logic increases code complexity
→ **Benefit:** Client compatibility improves dramatically; centralized logic easier to maintain than per-backend fixes
