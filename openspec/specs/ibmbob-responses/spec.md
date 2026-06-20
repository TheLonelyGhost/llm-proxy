## ADDED Requirements

### Requirement: IBM Bob returns 501 Not Implemented for /v1/responses

The IBM Bob provider SHALL NOT serve `/v1/responses`. Its
`backend.Backend.Responses` implementation SHALL return an error wrapping
`backend.ErrUnsupported`, which the server layer maps to HTTP 501
`not_implemented` with an OpenAI-shaped error envelope.

Rationale: the LiteLLM-compatible `/v1/responses` route is not reliably
available across Bob upstream deployments, and a silent translation to
`/v1/chat/completions` would mask reasoning-model behaviour differences.
Returning 501 lets clients fall back deterministically.

#### Scenario: Responses returns ErrUnsupported
- **WHEN** a caller invokes `Provider.Responses` on an IBM Bob backend
- **THEN** the provider SHALL return a nil response and an error such that
  `errors.Is(err, backend.ErrUnsupported)` is true, without performing any
  upstream HTTP request

#### Scenario: Server maps the error to HTTP 501
- **WHEN** a client sends `POST /v1/responses` with a model routed to an IBM
  Bob backend
- **THEN** the proxy SHALL respond with HTTP 501 and an OpenAI-shaped error
  envelope of type `not_implemented`
