## ADDED Requirements

### Requirement: Transform non-streaming text completion response

The system SHALL transform backend text completion responses to match OpenAI's `/v1/completions` response schema.

#### Scenario: Successful completion transformation

- **WHEN** backend returns HTTP 200 with text completion response body
- **THEN** system transforms response to include required fields: `id`, `object: "text_completion"`, `created`, `model`, `choices` array with `index`, `text`, `logprobs`, `finish_reason`, and `usage` object

#### Scenario: Backend response without logprobs

- **WHEN** backend returns completion response without `logprobs` field
- **THEN** transformed response includes `logprobs: null` in choices array

#### Scenario: Multiple completion choices

- **WHEN** backend returns response with `n > 1` choices
- **THEN** system transforms all choices preserving array order and setting correct `index` values

### Requirement: Preserve model routing prefix in completions

The system SHALL preserve the original prefixed model name in transformed completion responses.

#### Scenario: Completion with prefixed model

- **WHEN** client requests model `bob/llama-3` and backend returns response with model `llama-3`
- **THEN** transformed response SHALL include model field as `bob/llama-3`

### Requirement: Handle malformed completion responses

The system SHALL handle malformed completion responses without server crashes.

#### Scenario: Missing choices array

- **WHEN** backend returns completion response without `choices` array
- **THEN** system returns HTTP 502 with OpenAI-format error: `{"error": {"message": "upstream response missing required field: choices", "type": "upstream_error", "code": null}}`
