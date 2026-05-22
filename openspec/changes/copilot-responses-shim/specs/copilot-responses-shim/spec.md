## ADDED Requirements

### Requirement: Copilot translates /v1/responses to /chat/completions

The Copilot provider SHALL implement `backend.Backend.Responses` by
translating the OpenAI `/v1/responses` request body into an OpenAI
`/chat/completions` request body, dispatching that to
`POST /chat/completions` on the configured Copilot upstream, and translating
the response back into OpenAI Responses API shape. The provider SHALL NOT
return `ErrUnsupported` for `/v1/responses`.

#### Scenario: Plain text round-trip (non-streaming)
- **WHEN** the client sends `POST /v1/responses` with
  `input: "Say hi"`, no tools, `stream: false`
- **THEN** the provider SHALL issue `POST /chat/completions` with
  `messages: [{role:"user", content:"Say hi"}]`
- **AND** SHALL return a JSON body whose top-level fields include
  `object: "response"`, `status: "completed"`, `model: <requested-model>`,
  and `output` is an array containing exactly one item of type `"message"`
  with `content: [{type:"output_text", text:<assistant-content>}]`

#### Scenario: Array input is flattened to chat messages
- **WHEN** the client sends `input: [{role:"system", content:"..."},
  {role:"user", content:[{type:"input_text", text:"Hi"}]}]`
- **THEN** the upstream `messages` array SHALL contain the system message
  followed by a user message whose `content` is the string `"Hi"`
- **AND** input items with mixed content parts (e.g. multiple `input_text`
  fragments) SHALL be forwarded as a content-parts array on the chat message

### Requirement: Function-tool round-trip

The shim SHALL preserve OpenAI function tool calls in both directions so
agentic clients (e.g. OpenCode) can drive a tool loop.

#### Scenario: Tool definitions are reshaped for chat completions
- **WHEN** the client sends `tools: [{type:"function", name:"bash",
  description:"...", parameters:{...}}]`
- **THEN** the upstream `tools` array SHALL be
  `[{type:"function", function:{name:"bash", description:"...",
  parameters:{...}}}]`
- **AND** the client's `tool_choice` SHALL be forwarded verbatim

#### Scenario: tool_calls become function_call output items (non-streaming)
- **WHEN** the upstream returns a `choices[0].message.tool_calls` array
- **THEN** each entry SHALL produce a corresponding
  `output[].type = "function_call"` item carrying `id`, `call_id`, `name`,
  and the JSON-stringified `arguments`

### Requirement: Streaming emits the named Responses event sequence

When `stream: true`, the shim SHALL convert the upstream
`chat.completion.chunk` SSE stream into the OpenAI Responses event sequence
incrementally (no buffering of the full response).

#### Scenario: Lifecycle events are emitted in order
- **WHEN** the upstream returns a streaming chat completion that produces
  text content followed by `[DONE]`
- **THEN** the proxy SHALL emit, in this order:
  - `event: response.created`
  - `event: response.in_progress`
  - `event: response.output_item.added` (for the assistant message)
  - `event: response.content_part.added` (for the `output_text` part)
  - one `event: response.output_text.delta` per content chunk
  - `event: response.output_text.done`
  - `event: response.content_part.done`
  - `event: response.output_item.done`
  - `event: response.completed`

#### Scenario: Function-call streaming deltas
- **WHEN** the upstream emits `delta.tool_calls[].function.arguments`
  fragments across multiple chunks
- **THEN** the proxy SHALL emit one `event: response.output_item.added`
  (with `item.type: "function_call"`) followed by one
  `event: response.function_call_arguments.delta` per fragment, then
  `event: response.function_call_arguments.done` and
  `event: response.output_item.done` when the call closes

#### Scenario: Usage in response.completed
- **WHEN** the upstream supplies a final `usage` object (the shim sets
  `stream_options.include_usage = true`)
- **THEN** the `response.completed` event's `response.usage` SHALL contain
  `input_tokens`, `output_tokens`, and `total_tokens` populated from the
  upstream `prompt_tokens`, `completion_tokens`, and `total_tokens`

### Requirement: Allow-list and auth match the chat path

The shim SHALL reuse the same allow-list check and the same auth/token
refresh path as `Provider.Chat`. No additional auth surface is added.

#### Scenario: Allow-list enforced
- **WHEN** the requested model is not in the provider's allow-list
- **THEN** the provider SHALL return an error wrapping
  `backend.ErrUnknownModel` without contacting the upstream

#### Scenario: 401 triggers a single token refresh
- **WHEN** the upstream returns 401 on the first attempt
- **THEN** the provider SHALL invalidate the cached Copilot token and retry
  exactly once, identical to the existing chat-completions behaviour

### Requirement: Transform layer passes through real Responses bodies

`internal/transform.TransformResponsesResponse` (non-streaming branch) SHALL
detect bodies that already carry a top-level `output` array and pass them
through with only the `model` field rewritten to the backend-prefixed value.
The existing `choices`-based branch is retained for backward compatibility
with backends that still emit chat-shaped JSON on `/v1/responses`.

#### Scenario: Spec-shaped body is preserved
- **WHEN** the backend returns a JSON body with `object: "response"`,
  `output: [...]`, and `model: "gpt-4o"`
- **AND** the route was `copilot/gpt-4o`
- **THEN** the transform SHALL return that JSON unchanged except that
  `model` is rewritten to `"copilot/gpt-4o"`

#### Scenario: Chat-shaped body still works
- **WHEN** the backend returns a JSON body with `choices: [...]` and no
  `output` field
- **THEN** the transform SHALL apply the existing chat-style mapping for
  backward compatibility