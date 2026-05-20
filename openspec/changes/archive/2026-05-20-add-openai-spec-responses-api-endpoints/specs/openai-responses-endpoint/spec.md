## ADDED Requirements

### Requirement: Handle POST requests to /v1/responses

The system SHALL implement a `/v1/responses` endpoint that accepts POST requests for unified response generation.

#### Scenario: Successful responses request

- **WHEN** client sends POST to `/v1/responses` with valid request body containing `model` and `input` fields
- **THEN** system routes to appropriate backend and returns OpenAI-compliant response object

#### Scenario: Responses endpoint with streaming

- **WHEN** client sends POST to `/v1/responses` with `stream: true`
- **THEN** system returns SSE stream with response chunks in OpenAI format

#### Scenario: Request missing input field

- **WHEN** client sends POST to `/v1/responses` without `input` field
- **THEN** system returns HTTP 400 with error: `{"error": {"message": "request missing required field: input", "type": "invalid_request_error", "code": null}}`

### Requirement: Support reasoning object with effort parameter

The system SHALL accept and forward `reasoning` object with `effort` field for models with reasoning capabilities.

#### Scenario: Request with reasoning effort

- **WHEN** client sends request to `/v1/responses` with `reasoning: {"effort": "high"}`
- **THEN** system extracts reasoning effort value and includes it in backend request

#### Scenario: Request reasoning effort with non-reasoning model

- **WHEN** client sends request with `reasoning.effort` but backend does not support reasoning (returns `ErrUnsupported`)
- **THEN** system returns HTTP 400 with error: `{"error": {"message": "model does not support reasoning", "type": "invalid_request_error", "code": null}}`

#### Scenario: Invalid reasoning effort value

- **WHEN** client sends request with `reasoning.effort` not in `["low", "medium", "high"]`
- **THEN** system returns HTTP 400 with error: `{"error": {"message": "reasoning.effort must be one of: low, medium, high", "type": "invalid_request_error", "code": null}}`

#### Scenario: Request with empty reasoning object

- **WHEN** client sends request with `reasoning: {}` (no effort specified)
- **THEN** system treats as no reasoning parameter and forwards request normally

#### Scenario: Request with reasoning object containing only effort

- **WHEN** client sends request with `reasoning: {"effort": "medium"}`
- **THEN** system accepts and forwards effort value to backend

### Requirement: Transform responses endpoint output

The system SHALL transform backend responses from `/v1/responses` to match OpenAI response object schema.

#### Scenario: Non-streaming response transformation

- **WHEN** backend returns response object for non-streaming request
- **THEN** system transforms to include required fields: `id`, `object: "response"`, `created`, `model`, `choices` array with reasoning content and finish_reason, `usage` object including `reasoning_tokens` if applicable

#### Scenario: Streaming response transformation

- **WHEN** backend returns SSE stream for responses endpoint
- **THEN** each chunk SHALL be formatted as `data: <json>\n\n` where JSON contains `id`, `object: "response.chunk"`, `created`, `model`, `choices` with deltas

#### Scenario: Response includes reasoning output

- **WHEN** backend returns response with reasoning output
- **THEN** transformed response includes reasoning fields in choice object and `reasoning_tokens` in usage statistics

### Requirement: Add Responses method to Backend interface

The system SHALL extend the `backend.Backend` interface with a `Responses` method for unified response API.

#### Scenario: Backend implements Responses method

- **WHEN** backend implements `Responses(ctx, ResponsesRequest)` method
- **THEN** proxy routes `/v1/responses` requests to that backend's Responses implementation

#### Scenario: Backend does not support Responses

- **WHEN** backend's `Responses` method returns `backend.ErrUnsupported`
- **THEN** proxy returns HTTP 501 with error: `{"error": {"message": "responses endpoint not supported by this backend", "type": "not_implemented", "code": null}}`

### Requirement: Preserve model routing prefix in responses

The system SHALL preserve the original prefixed model name in responses endpoint output.

#### Scenario: Responses with prefixed model

- **WHEN** client requests model `copilot/o1-preview` and backend returns response with model `o1-preview`
- **THEN** transformed response SHALL include model field as `copilot/o1-preview`
