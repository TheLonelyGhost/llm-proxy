## 1. Create transformation package

- [x] 1.1 Create `internal/transform` package directory and initial files
- [x] 1.2 Define transformation function signatures in `transform.go`
- [x] 1.3 Add OpenAI response type definitions in `types.go`
- [x] 1.4 Create error type definitions for transformation failures

## 2. Implement non-streaming response transformers

- [x] 2.1 Implement `TransformChatResponse` for chat completions (handles JSON parsing, field mapping, model prefix preservation)
- [x] 2.2 Implement `TransformCompletionResponse` for text completions (handles choices array, logprobs field)
- [x] 2.3 Implement `TransformEmbeddingResponse` for embeddings (validates vector dimensions, handles batch inputs)
- [x] 2.4 Implement `TransformResponsesResponse` for `/v1/responses` endpoint (handles reasoning output, reasoning_tokens in usage)
- [x] 2.5 Add unit tests for each transformer with valid backend responses
- [x] 2.6 Add unit tests for malformed backend responses (missing fields, invalid JSON, empty arrays)

## 3. Implement error response transformer

- [x] 3.1 Implement `TransformErrorResponse` with HTTP status code to error type mapping
- [x] 3.2 Add logic to parse structured JSON errors from backends
- [x] 3.3 Add logic to handle plain text error responses
- [x] 3.4 Add logic to preserve `Retry-After` and other relevant headers
- [x] 3.5 Add unit tests for all HTTP status code mappings (400, 401, 403, 404, 429, 500, 502, 503, 504)

## 4. Implement streaming response transformer

- [x] 4.1 Create `streaming.go` with SSE parser using `bufio.Scanner`
- [x] 4.2 Implement `TransformStreamingChatResponse` for SSE chat completion chunks
- [x] 4.3 Implement `TransformStreamingCompletionResponse` for SSE text completion chunks
- [x] 4.4 Implement `TransformStreamingResponsesResponse` for SSE responses endpoint chunks with reasoning deltas
- [x] 4.5 Add logic to detect and transform `[DONE]` events
- [x] 4.6 Add logic to pass through SSE comment lines (`:` prefix) for keep-alive
- [x] 4.7 Add error handling for malformed SSE events (log and skip)
- [x] 4.8 Add error handling for premature stream closure (send error event and close)
- [x] 4.9 Add unit tests with mock SSE streams
- [x] 4.10 Add benchmark tests to verify no buffering on large streams (>1MB)

## 5. Update backend interface for model capabilities

- [x] 5.1 Add `TokenLimits` struct to `internal/backend/backend.go` with `MaxInputTokens`, `MaxOutputTokens`, `MaxContextTokens` fields (all int, optional)
- [x] 5.2 Extend `ModelInfo` in `internal/backend/backend.go` with `ReasoningEffortLevels []string` field (optional, for reasoning models)
- [x] 5.3 Extend `ModelInfo` in `internal/backend/backend.go` with `TokenLimits *TokenLimits` field (optional, pointer to allow nil)
- [x] 5.4 Change `Backend.Models()` signature in `internal/backend/backend.go` from `([]Model, error)` to `([]ModelInfo, error)`
- [x] 5.5 Remove `Model` type from `internal/backend/backend.go` (replaced by `ModelInfo`)
- [x] 5.6 Mark `ModelLister` interface as deprecated (backends now use `Models()` for both use cases)
- [x] 5.7 Update Copilot backend: parse token limits from `capabilities.limits` in upstream response
- [x] 5.8 Update Copilot backend: change `Models()` to delegate to `ListUpstreamModels()` implementation
- [x] 5.9 Update OpenAI backend: extract token limits from upstream metadata if available
- [x] 5.10 Update OpenAI backend: populate `ReasoningEffortLevels` for o1/o3 models (["low", "medium", "high"])
- [x] 5.11 Update OpenAI backend: change `Models()` to delegate to `ListUpstreamModels()` implementation
- [x] 5.12 Update IBM Bob backend: extract token limits from model info endpoint
- [x] 5.13 Update IBM Bob backend: change `Models()` to delegate to `ListUpstreamModels()` implementation
- [x] 5.14 Verify all backends derive capabilities and limits from upstream provider responses
- [x] 5.15 Add unit tests for `Models()` returning `ModelInfo` with capabilities, token limits, reasoning effort levels

## 6. Add model capability discovery endpoints

- [x] 6.1 Update `/v1/models` handler in `internal/server/handlers.go` to return exact OpenAI spec format (no capabilities)
- [x] 6.2 Add `handleModelInfo` handler for `/v1/model/info` endpoint that returns ModelInfo with capabilities, token_limits, reasoning_effort_levels
- [x] 6.3 Define `modelInfoItem` struct with id, name, vendor, capabilities, token_limits, reasoning_effort_levels, deprecated, version, extras fields
- [x] 6.4 Register `/v1/model/info` route in `internal/server/server.go`
- [x] 6.5 Ensure both endpoints use `Backend.Models()` (now returns ModelInfo)
- [x] 6.6 Add unit tests for `/v1/models` conformance to OpenAI spec (no extra fields)
- [x] 6.7 Add unit tests for `/v1/model/info` including capability metadata, token limits, reasoning effort levels
- [x] 6.8 Add integration test verifying model-specific capability differences (e.g., copilot/gpt-4o vs openai/gpt-4o)

## 7. Add /v1/responses endpoint support

- [x] 7.1 Add `Reasoning` struct to `internal/backend/backend.go` with `Effort` string field
- [x] 7.2 Add `ResponsesRequest` type to `internal/backend/backend.go` with Model, Stream, Input, Reasoning (optional), Body fields
- [x] 7.3 Add `Responses(ctx context.Context, req ResponsesRequest) (*Response, error)` method to Backend interface
- [x] 7.4 Implement `Responses` method in all backends (GitHub Copilot, IBM Bob, OpenAI) - return ErrUnsupported if not applicable
- [x] 7.5 Add `handleResponses` handler in `internal/server/handlers.go` that extracts `input` and `reasoning.effort` from request body
- [x] 7.6 Add validation for `input` field (required) and `reasoning.effort` (must be "low", "medium", or "high" if present)
- [x] 7.7 Register `/v1/responses` route in `internal/server/server.go`
- [x] 7.8 Add unit tests for request parsing with `input` field
- [x] 7.9 Add unit tests for `reasoning.effort` parameter validation (valid values, invalid values, empty reasoning object)
- [x] 7.10 Add unit tests for ErrUnsupported handling from backends

## 8. Integrate transformers into server handlers

- [x] 8.1 Update `handleChat` in `internal/server/handlers.go` to call chat transformer
- [x] 8.2 Update `handleCompletion` to call completion transformer
- [x] 8.3 Update `handleEmbedding` to call embedding transformer
- [x] 8.4 Update `handleResponses` to call responses transformer
- [x] 8.5 Update `writeUpstreamResponse` to call appropriate transformer based on endpoint type
- [x] 8.6 Add logic to detect streaming vs non-streaming responses
- [x] 8.7 Ensure model prefix is passed to all transformers for preservation

## 9. Update error handling

- [x] 9.1 Update `writeUpstreamResponse` error cases to use `TransformErrorResponse`
- [x] 9.2 Ensure all backend errors are transformed before writing to client
- [x] 9.3 Add fallback for unparseable backend responses (return 502 with generic error)

## 10. Testing

- [x] 10.1 Add integration tests in `internal/server/server_test.go` with mock backends returning non-conformant responses
- [x] 10.2 Add integration tests for each endpoint (chat, completion, embedding, responses) verifying OpenAI spec compliance
- [x] 10.3 Add integration tests for `/v1/responses` with reasoning_effort parameter
- [x] 10.4 Add integration tests for `/v1/models` exact spec conformance (no extra fields)
- [x] 10.5 Add integration tests for `/v1/model/info` with capabilities
- [x] 10.6 Add integration tests for error responses from backends
- [x] 10.7 Add integration tests for streaming responses with multiple chunks
- [x] 10.8 Test with actual backend responses from GitHub Copilot, IBM Bob, OpenAI (capture fixtures if needed)
- [x] 10.9 Run `task test:cover` to verify >= 80% coverage threshold

## 11. Documentation

- [x] 11.1 Update `README.md` to mention `/v1/responses` endpoint and reasoning effort support
- [x] 11.2 Update `README.md` to mention `/v1/model/info` capability discovery endpoint
- [x] 11.3 Update `README.md` to mention OpenAI API spec compliance
- [x] 11.4 Add godoc comments to all exported functions in `internal/transform`
- [x] 11.5 Update `docs/configuration.md` if any configuration changes needed
- [x] 11.6 Document capability field values and their meanings (chat, tools, vision, reasoning, streaming, embeddings, etc.)
