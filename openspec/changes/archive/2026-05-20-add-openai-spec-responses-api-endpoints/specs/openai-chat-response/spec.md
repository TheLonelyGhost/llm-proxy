## ADDED Requirements

### Requirement: Transform non-streaming chat completion response

The system SHALL transform backend chat completion responses to match OpenAI's `/v1/chat/completions` response schema.

#### Scenario: Successful chat completion transformation

- **WHEN** backend returns HTTP 200 with chat completion response body
- **THEN** system transforms response to include required fields: `id`, `object: "chat.completion"`, `created`, `model`, `choices` array with `index`, `message` with `role` and `content`, `finish_reason`, and `usage` object

#### Scenario: Backend response missing optional fields

- **WHEN** backend returns response without `usage` or `system_fingerprint` fields
- **THEN** system includes those fields with null values in transformed response

#### Scenario: Backend response has extra fields

- **WHEN** backend returns response with provider-specific extra fields
- **THEN** system preserves extra fields in transformed response while ensuring all required OpenAI fields are present

### Requirement: Preserve model routing prefix

The system SHALL preserve the original prefixed model name in transformed responses.

#### Scenario: Model name includes routing prefix

- **WHEN** client requests model `copilot/gpt-4o` and backend returns response with model `gpt-4o`
- **THEN** transformed response SHALL include model field as `copilot/gpt-4o`

### Requirement: Handle malformed backend responses

The system SHALL handle malformed backend responses gracefully without causing server errors.

#### Scenario: Backend returns unparseable JSON

- **WHEN** backend returns HTTP 200 with invalid JSON body
- **THEN** system returns HTTP 502 with OpenAI-format error: `{"error": {"message": "upstream returned invalid response", "type": "upstream_error", "code": null}}`

#### Scenario: Backend returns JSON missing required fields

- **WHEN** backend returns valid JSON but missing required fields like `choices`
- **THEN** system returns HTTP 502 with OpenAI-format error describing missing fields
