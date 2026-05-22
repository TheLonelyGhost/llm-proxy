## Context

OpenCode's "GitHub Copilot via OpenAI" integration is implemented in OpenCode
itself as Chat Completions calls against `api.githubcopilot.com`. The Copilot
upstream does **not** expose `/v1/responses`. To make this proxy a compliant
OpenAI `/v1/responses` front door for `copilot/<model>`, the request must be
fanned out to `/chat/completions` and the response folded back into the
Responses API shape. The translation lives entirely in the Copilot provider
so the rest of the proxy is unaware of it.

## Goals / Non-Goals

**Goals:**
- Accept the full OpenAI `/v1/responses` request schema we already validate at
  the server boundary (string and array `input`, function `tools`, `stream`,
  `reasoning.effort`, `max_output_tokens`, `temperature`, `top_p`,
  `instructions`, `metadata`, `tool_choice`).
- Emit OpenAI-spec response bodies (`object: "response"`, `output: [...]`) and
  the named Responses SSE event sequence when `stream: true`.
- Preserve function tool-call round-trips so OpenCode can drive its tool loop.
- Preserve streaming back-pressure: do not buffer the full upstream stream
  before emitting events.

**Non-Goals:**
- Built-in tools (`web_search`, `image_generation`, `code_interpreter`, file
  search). Copilot's chat surface doesn't expose them, and synthesising them
  would mislead clients.
- `previous_response_id` and `store: true`. The proxy is stateless and does
  not persist responses. Pass-through with `store: true` is allowed but
  ignored.
- Response retrieval/deletion routes (`GET /v1/responses/{id}`, DELETE).
  Out of scope for this change; can be a follow-up if a use case appears.
- `reasoning.effort` is advisory: it is included in the chat-completions body
  verbatim so models that recognise it can act, but the proxy does not fail
  if Copilot ignores it.
- Encrypted content / multi-deployment affinity. The proxy does not load
  balance Copilot.

## Decisions

### D1. Translation lives in the Copilot provider, not the server

`copilot.Provider.Responses` does the entire request→chat→response→Responses
cycle. The server's `handleResponses` calls `provider.Responses` exactly as
it does for any other backend and applies its existing transform pipeline.

**Alternatives considered:**
- Server-level shim: server detects "responses-on-copilot" and rewrites the
  request to `/chat/completions` then re-shapes. Rejected — it would couple
  the server to Copilot's API shape and prevent other backends from
  customising the translation differently.

### D2. Non-streaming transform pass-through for real Responses bodies

`transform.TransformResponsesResponse` non-streaming branch currently parses
upstream JSON looking for `choices` and re-emits a fictional Responses shape.
That made sense when no backend produced spec-shaped bodies; now Copilot will.
The branch is updated to detect `output` (real Responses shape) and pass
through with only a `model` rewrite. The `choices`-based path is kept for
backward compatibility.

**Alternatives considered:**
- Replace the legacy branch entirely. Rejected — it would break any test
  fixture or out-of-tree backend that still emits chat-shaped JSON on
  `/v1/responses`. Additive detection is safer.

### D3. Streaming: provider emits Responses SSE directly

The Copilot provider wraps the upstream `chat.completion.chunk` SSE stream in
a translator that emits the Responses event sequence on the fly. The server's
existing `TransformStreamingResponsesResponse` (already a pass-through after
the previous change) forwards the stream unchanged.

This keeps the streaming responsibility in one place per backend and avoids
adding a new "chat-to-responses streaming" knob to the server.

### D4. Tool-call mapping

- **Request** `tools: [{type:"function", name, description, parameters}]` →
  `tools: [{type:"function", function:{name, description, parameters}}]`.
  `tool_choice` is forwarded as-is.
- **Non-streaming response** chat `tool_calls: [{id, function:{name,
  arguments}}]` → Responses `output: [{type:"function_call", id, call_id,
  name, arguments}]` items, in addition to any text message item.
- **Streaming response** chat `delta.tool_calls[].function.arguments` chunks
  → `response.function_call_arguments.delta` events with matching `item_id`
  and `output_index`. A `response.function_call_arguments.done` event closes
  the call, followed by `response.output_item.done`.

### D5. IDs and timestamps

- The Responses `id` is derived from the upstream `chatcmpl-XXX` id, prefixed
  `resp_` (so `resp_chatcmpl-XXX`). This is purely cosmetic; the proxy does
  not store anything keyed on it.
- `created_at` is sourced from the upstream `created` if present, else
  `time.Now().Unix()`.
- Message item ids are `msg_<resp-id>`; function-call item ids are
  `fc_<resp-id>_<index>`. These are deterministic per response so downstream
  events can refer to them by `item_id`.

### D6. Streaming `usage` field

`/chat/completions` only emits final `usage` when `stream_options.include_usage=true`
is set. The shim sets that flag transparently so the final `response.completed`
event can include `usage` translated to Responses field names
(`input_tokens`, `output_tokens`, `total_tokens`).

## Risks / Trade-offs

- **Risk**: OpenAI evolves the Responses event sequence (new event types,
  new fields). The shim covers the subset OpenCode and the official Python /
  JS SDKs require today. → **Mitigation**: implement the events from the
  current OpenAI Responses streaming spec; add a fuzz target on the
  translator so new field shapes don't crash it; document the supported
  event set.
- **Risk**: Copilot returns content shapes the shim doesn't anticipate (e.g.
  refusal messages, image parts). → **Mitigation**: the shim falls back to
  emitting `output_text` with the raw `delta.content` and logs unknown
  shapes at DEBUG. Refusals are surfaced as `output_text` so the client
  still sees the model's words.
- **Risk**: Buffering an entire chat-completions response in the shim's
  streaming path would defeat SSE back-pressure. → **Mitigation**: the
  streaming translator is a streaming reader — one `scan + decode + emit`
  per chunk, no global accumulator beyond a per-call function-call argument
  buffer.
- **Trade-off**: Without `previous_response_id` support, multi-turn
  conversations must be expressed by the client as a self-contained `input`
  array on each request. This matches how OpenCode actually uses the API
  today (it carries its own history), but is worth calling out in docs.

## Migration Plan

1. Land the change. `/v1/responses` against `copilot/<model>` immediately
   returns OpenAI-compliant bodies and SSE events.
2. No data or config migration. No flag flips. Rollback is a single revert.
3. After release, watch for upstream Copilot behaviour drift in the
   chat-completion error envelope (the shim re-uses the same 502 path as
   chat).

## Open Questions

- Should the proxy synthesise `response.in_progress` events between
  `response.created` and the first content delta? OpenCode tolerates either,
  but verbose SSE clients may expect it. Decision: emit a single
  `response.in_progress` after `response.created` for parity with OpenAI.
  Tracked in tasks.