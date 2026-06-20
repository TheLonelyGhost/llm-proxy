## ADDED Requirements

### Requirement: Accept OpenAI-compliant `input` shapes

The `/v1/responses` handler SHALL accept the `input` field as either a JSON
string or a JSON array of objects, matching OpenAI's published Responses API
schema.

#### Scenario: String input is accepted
- **WHEN** a client sends `POST /v1/responses` with a body whose `input` field is
  a JSON string
- **THEN** the proxy SHALL forward the request to the resolved backend without
  returning 400 for the `input` field

#### Scenario: Array-of-items input is accepted
- **WHEN** a client sends `POST /v1/responses` with a body whose `input` field is
  a JSON array of objects (each item shaped like
  `{"role": "...", "content": [...]}` or any other OpenAI-defined input item)
- **THEN** the proxy SHALL forward the request to the resolved backend with the
  `input` array preserved byte-for-byte in the rewritten body

#### Scenario: Missing input is rejected
- **WHEN** a client sends `POST /v1/responses` with no `input` field, a
  JSON-null `input`, or an empty-string `input`
- **THEN** the proxy SHALL respond with HTTP 400 and an `invalid_request_error`
  envelope whose message identifies the missing field as `"input"`

#### Scenario: Wrong-kind input is rejected
- **WHEN** a client sends `POST /v1/responses` with an `input` field that is
  neither a JSON string nor a JSON array (e.g. a number, boolean, or object), or
  with an `input` array whose elements are not JSON objects
- **THEN** the proxy SHALL respond with HTTP 400 and an `invalid_request_error`
  envelope whose message identifies the accepted shapes

### Requirement: Reasoning effort allow-list matches OpenAI

If a `reasoning.effort` value is present in the request, the proxy SHALL accept
exactly the OpenAI-defined set `{"minimal", "low", "medium", "high"}` and reject
all other values.

#### Scenario: Minimal effort accepted
- **WHEN** the request contains `"reasoning": {"effort": "minimal"}`
- **THEN** the proxy SHALL forward the request to the backend

#### Scenario: Unknown effort rejected
- **WHEN** the request contains `"reasoning": {"effort": "<other>"}` for any
  value not in the allow-list
- **THEN** the proxy SHALL respond with HTTP 400 and an `invalid_request_error`
  envelope naming the accepted values

### Requirement: Error envelope for unsupported and upstream errors

The handler SHALL preserve the existing error mapping for `/v1/responses`:
backend errors wrapping `backend.ErrUnsupported` surface as HTTP 501
`not_implemented`; unknown-model errors surface as HTTP 404
`invalid_request_error`; other backend errors surface as HTTP 502
`upstream_error`.

#### Scenario: Backend lacks /v1/responses
- **WHEN** the resolved backend's `Responses` method returns an error wrapping
  `backend.ErrUnsupported`
- **THEN** the proxy SHALL respond with HTTP 501 and an OpenAI-shaped error
  envelope of type `not_implemented`

#### Scenario: Upstream failure
- **WHEN** the backend returns a non-nil error that is not `ErrUnsupported` or
  `ErrUnknownModel`
- **THEN** the proxy SHALL respond with HTTP 502 and an OpenAI-shaped
  `upstream_error` envelope

### Requirement: Streaming semantics match other endpoints

When the request body sets `"stream": true`, the handler SHALL stream the
backend response body through unchanged, with the upstream `Content-Type`
preserved (typically `text/event-stream`).

#### Scenario: Streamed request is streamed back
- **WHEN** a client sends `POST /v1/responses` with `"stream": true` and the
  backend returns a streaming response
- **THEN** the proxy SHALL write the response body incrementally to the client
  without buffering it in full
