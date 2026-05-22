## ADDED Requirements

### Requirement: Bob translates /v1/responses via the shared shim

The IBM Bob provider SHALL implement `backend.Backend.Responses` by
delegating request and response translation to the shared
`internal/responsesshim` package and dispatching the translated body
through the same chat-completions path used by `Chat` (authn backend:
`/inference/v1/chat/completions`; legacy backend: `/chat/completions`).
The provider SHALL NOT return `ErrUnsupported` for `/v1/responses`.

#### Scenario: Request body is translated to chat-completions
- **WHEN** the client sends `POST /v1/responses` with `input: "Say hi"` and
  `max_output_tokens: 32`
- **THEN** the provider SHALL POST to the chat-completions path with a body
  whose `messages` array contains the user message and whose `max_tokens`
  is 32

#### Scenario: Non-streaming response is shaped as a Responses object
- **WHEN** the upstream returns a `chat.completion` JSON body
- **THEN** the provider SHALL return a body whose top level is
  `{object:"response", status:"completed", model:<requested>, output:[...]}`
  with the assistant text wrapped in a `message`/`output_text` part

#### Scenario: Streaming emits OpenAI named events
- **WHEN** `stream: true` and the upstream emits a `chat.completion.chunk`
  SSE stream with `[DONE]`
- **THEN** the provider's response `Content-Type` SHALL be
  `text/event-stream` and the body SHALL contain `response.created`,
  `response.in_progress`, `response.output_item.added`,
  `response.content_part.added`, one or more `response.output_text.delta`,
  `response.output_text.done`, `response.content_part.done`,
  `response.output_item.done`, and `response.completed`, in that order

### Requirement: Bob auth, identity, and signing apply unchanged

The shim SHALL reuse Bob's existing dispatch path. Allow-list enforcement,
identity bootstrap (`/admin/v1/profile`), Authorization header selection,
HMAC signing (on the modern backend), and retry behaviour SHALL apply to
`/v1/responses` requests identically to `/v1/chat/completions`.

#### Scenario: Allow-list rejection short-circuits before translation
- **WHEN** the requested model is not in the provider's allow-list
- **THEN** the provider SHALL return an error wrapping
  `backend.ErrUnknownModel` without translating the body or contacting the
  upstream

#### Scenario: Authn backend signs and propagates identity
- **WHEN** the provider is configured with a JWT key and `hmac_secret`
- **THEN** every upstream chat-completions request issued by `Responses`
  SHALL carry `Authorization`, `x-instance-id`, `x-team-id`,
  `x-request-timestamp`, and `x-request-signature` headers, identical to
  `Chat`

### Requirement: Legacy key path uses the legacy chat endpoint

When the configured API key kind is legacy, the provider SHALL dispatch
`/v1/responses` to the legacy chat path (`/chat/completions`) and SHALL NOT
fetch `/admin/v1/profile`.

#### Scenario: Legacy key skips profile fetch
- **WHEN** a legacy `sk-*` key drives a `Responses` call
- **THEN** no `/admin/v1/profile` request SHALL be made
- **AND** the upstream chat path SHALL be `/chat/completions`

### Requirement: Upstream non-2xx is passed through

When the upstream chat-completions response is outside `[200, 300)`, the
provider SHALL return the upstream `*backend.Response` unchanged so the
server's existing error envelope handling applies. The shim SHALL NOT be
invoked on error bodies.

#### Scenario: Upstream 502 is forwarded
- **WHEN** the upstream returns HTTP 502 with an error body
- **THEN** the provider's returned `Response.StatusCode` SHALL be 502 and
  its `Body` SHALL be the upstream body
