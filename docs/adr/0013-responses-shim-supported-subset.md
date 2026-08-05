# Responses shim supported subset: no stateful features, ids are informational only

The Responses shim (`internal/responsesshim`) implements a specific, fixed subset
of the OpenAI Responses API. Features that require server-side state, built-in
tools, or upstream Responses API support are explicitly out of scope and will
produce a 400 or be silently dropped, as described below.

## Supported

- `input` as a string or array of input items (role/content/type).
- `instructions` at the top level, prepended as a `system` message.
- `tools` of type `function` (`{type, name, description, parameters}`);
  `tool_choice` forwarded verbatim.
- `temperature`, `top_p`, `max_output_tokens` (→ `max_tokens`), `metadata`,
  `parallel_tool_calls`, `user`.
- `stream: true` — translated chunk-by-chunk into the OpenAI named-event
  sequence. Function-call deltas produce the `response.function_call_arguments`
  event sub-sequence.
- `reasoning.effort` — extracted and forwarded as the flat `reasoning_effort`
  string on the translated chat request (see ADR-0012). Copilot and IBM Bob
  upstreams that do not reason ignore it; reasoning-capable upstreams honour it.

## Not supported (explicit non-goals)

- **`previous_response_id`** — the proxy is stateless; there is no response
  store to look up. Clients must carry their full conversation history on every
  `input` array.
- **`store: true` / response retrieval / deletion** (`GET /v1/responses/{id}`,
  `DELETE /v1/responses/{id}`) — same reason. Response ids emitted by the shim
  are informational only.
- **Built-in tools** (`web_search`, `image_generation`, `code_interpreter`,
  file search) — Copilot's and Bob's chat surfaces do not accept them; they are
  silently dropped from the translated request.
- **Encrypted content / multi-deployment affinity** — no upstream support.
- **`reasoning.summary`, `reasoning.mode`, `reasoning.context`** — no Chat
  Completions equivalents; dropped during translation (see ADR-0012).

## Response id derivation

Every response emitted by the shim carries an `id` of the form
`resp_<chatcmpl-id>`, where `<chatcmpl-id>` is the upstream chat completion id.
These ids are informational and cannot be used to retrieve the response later.
Clients that need multi-turn continuity must pass the full history as an `input`
array on each request.

## Consequences

- Agentic clients that rely on `previous_response_id` for multi-turn state
  management must maintain their own history when targeting shimmed backends.
- New Responses API features must be explicitly added to the shim before
  shimmed backends can use them. The shim does not speculatively forward unknown
  fields that have no chat equivalent.
- Backends that gain native Responses API support bypass the shim entirely (see
  ADR-0006); the non-goals above do not apply to native-support backends.

## Considered Options

- **Forward unknown Responses API fields to the chat body** — rejected.
  Produces non-conformant chat requests and creates a silent correctness hazard
  when the upstream ignores the unknown fields.
- **Return 400 for any unsupported field** — rejected. Overly strict; breaks
  clients that send benign optional fields (e.g. `metadata`). Unsupported
  _stateful_ features are the meaningful boundary; unknown scalar fields are
  forwarded verbatim.
