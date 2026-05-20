## Why

The proxy currently forwards backend responses as-is without conforming to the OpenAI API specification. This causes clients that expect strict OpenAI-compliant responses to fail or behave incorrectly when the upstream backend returns non-conformant data structures or error formats.

## What Changes

- Add response validation and transformation layer between backend responses and client
- Implement OpenAI API spec-compliant response formatting for `/v1/chat/completions`, `/v1/completions`, `/v1/embeddings`, `/v1/responses`
- Add `/v1/responses` endpoint for unified response objects with `input` field and nested `reasoning.effort` parameter
- Add `/v1/model/info` endpoint for model capability discovery (proxy extension, not in OpenAI spec)
- Maintain `/v1/models` exact OpenAI spec compliance (no added fields)
- Change Backend interface to expose model capabilities derived from upstream providers
- Standardize error response format to match OpenAI's `{"error": {"message": "...", "type": "...", "code": "..."}}` structure
- Add response streaming normalization to ensure SSE events match OpenAI's `data: {...}\n\n` format
- Support `reasoning.effort` parameter ("low", "medium", "high") for models with reasoning capabilities
- Preserve existing routing and backend selection behavior

## Capabilities

### New Capabilities
- `openai-chat-response`: Transform backend chat completion responses to OpenAI spec format
- `openai-completion-response`: Transform backend text completion responses to OpenAI spec format
- `openai-embedding-response`: Transform backend embedding responses to OpenAI spec format
- `openai-responses-endpoint`: Add `/v1/responses` endpoint with `input` field and nested `reasoning.effort` parameter
- `openai-model-capability-discovery`: Add `/v1/model/info` endpoint exposing per-model capabilities derived from upstream providers
- `openai-error-response`: Standardize error responses to OpenAI spec format
- `openai-streaming-response`: Normalize SSE streaming to OpenAI spec format

### Modified Capabilities

## Impact

- `internal/server/handlers.go`: Add response transformation logic before writing to client; add `/v1/responses` handler with `input` and `reasoning.effort` parsing; add `/v1/model/info` handler for capability discovery
- `internal/server/server.go`: Register `/v1/responses` and `/v1/model/info` routes in mux
- `internal/backend/backend.go`: Change `Models()` to return `[]ModelInfo` instead of `[]Model`; add `Responses` method to Backend interface; add `Reasoning` struct with `Effort` field; deprecate `Model` type
- All backend implementations: Update `Models()` to delegate to `ListUpstreamModels()`; implement new `Responses` method (may return `ErrUnsupported` if reasoning not available)
- `/v1/models` endpoint: Maintain exact OpenAI spec format (no capabilities field)
- `/v1/model/info` endpoint: Return rich metadata including capabilities, vendor, version
- Client compatibility: Improves interoperability with OpenAI SDK clients; enables graceful degradation via capability discovery
