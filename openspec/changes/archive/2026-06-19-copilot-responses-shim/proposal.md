## Why

`/v1/responses` against `copilot/<model>` currently returns HTTP 501. OpenCode
and other OpenAI-SDK clients increasingly default to the Responses API for
agentic workflows (tool calling, reasoning), so the proxy needs to expose a
compliant `/v1/responses` surface for Copilot.

GitHub Copilot's upstream (`api.githubcopilot.com`) does not implement
`/v1/responses`; the only OpenAI-shaped surface it serves is
`/chat/completions`. To unblock OpenCode while keeping the wire protocol
OpenAI-compliant, the Copilot provider gets an in-proxy translation shim that
maps `/v1/responses` requests to `/chat/completions` and translates the chat
response (both non-streaming JSON and SSE) back into the Responses API event
shape.

## What Changes

- Implement `copilot.Provider.Responses` as a translation shim:
  - **Request mapping**: convert OpenAI `/v1/responses` body to a
    `/chat/completions` body, then call the existing Copilot chat path with
    the same auth and streaming semantics.
  - **Non-streaming response mapping**: convert the upstream
    `chat.completion` JSON into an OpenAI `response` object with an `output`
    array (message + optional `function_call` items), `usage` mapped to
    Responses field names, and the original `model` echoed back.
  - **Streaming response mapping**: convert the upstream
    `chat.completion.chunk` SSE into the OpenAI named-event sequence
    (`response.created` → `response.output_item.added` →
    `response.content_part.added` → `response.output_text.delta`* →
    `response.output_text.done` → `response.content_part.done` →
    `response.output_item.done` → `response.completed`). Function-call deltas
    emit the `response.function_call_arguments.{delta,done}` sequence.
- Update `internal/transform/transform.go::TransformResponsesResponse` so the
  non-streaming branch detects bodies that are already in real Responses
  shape (presence of top-level `output` array) and passes them through with
  only `model` rewriting. The existing chat-style branch is kept for any
  backend that still emits `choices`.
- Update `docs/providers/github-copilot.md` to document `/v1/responses`
  support, the supported subset (text/messages, function tools, streaming),
  and the explicit non-goals (no built-in tools, no response ID retrieval, no
  `previous_response_id`, `reasoning.effort` advisory-only).

## Capabilities

### New Capabilities
- `copilot-responses-shim`: the Copilot provider's `/v1/responses`
  translation layer — request mapping, non-streaming response mapping,
  streaming event-sequence generation, and tool-call mapping.

### Modified Capabilities
<!-- None: openspec/specs/ has no existing spec for the responses-endpoint
     capability yet (the first change defining it is still in flight). The
     transform-layer pass-through is captured under the new
     copilot-responses-shim capability. -->

## Impact

- **Code**:
  - `internal/providers/copilot/copilot.go` — real `Responses` implementation.
  - `internal/providers/copilot/responses_shim.go` (new) — request/response/SSE
    translators.
  - `internal/transform/transform.go` — non-streaming pass-through for
    already-shaped Responses bodies.
- **Tests**:
  - `internal/providers/copilot/responses_shim_test.go` — unit tests for each
    translator plus an `httptest.Server` end-to-end that proves the upstream
    receives a `/chat/completions` body and the client sees Responses events.
- **Docs**: `docs/providers/github-copilot.md` documents the supported
  subset and explicit non-goals.
- **Dependencies**: none added.
- **State**: none added; endpoint remains stateless. No response IDs are
  persisted; the `id` emitted in `response.created` is derived from the
  upstream `chatcmpl-*` id (prefixed `resp_`) and is not retrievable.
- **Breaking**: none. Bob continues to return 501 (no change). The transform
  pass-through is additive: bodies still containing `choices` follow the
  existing path.