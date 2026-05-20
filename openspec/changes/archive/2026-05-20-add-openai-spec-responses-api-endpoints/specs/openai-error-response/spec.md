## ADDED Requirements

### Requirement: Transform backend error responses to OpenAI format

The system SHALL transform all backend error responses to match OpenAI's error response schema: `{"error": {"message": "...", "type": "...", "code": null}}`.

#### Scenario: Backend returns structured error

- **WHEN** backend returns HTTP 4xx or 5xx with JSON body containing error message
- **THEN** system extracts message and transforms to OpenAI format with appropriate `type` field

#### Scenario: Backend returns plain text error

- **WHEN** backend returns HTTP error with plain text body
- **THEN** system wraps text in OpenAI error format: `{"error": {"message": "<text>", "type": "upstream_error", "code": null}}`

#### Scenario: Backend returns empty error body

- **WHEN** backend returns HTTP error with empty response body
- **THEN** system generates OpenAI error: `{"error": {"message": "upstream returned error with no details", "type": "upstream_error", "code": null}}`

### Requirement: Map HTTP status codes to error types

The system SHALL map backend HTTP status codes to appropriate OpenAI error types.

#### Scenario: Backend returns 400 Bad Request

- **WHEN** backend returns HTTP 400
- **THEN** transformed error includes `type: "invalid_request_error"`

#### Scenario: Backend returns 401 Unauthorized

- **WHEN** backend returns HTTP 401
- **THEN** transformed error includes `type: "authentication_error"`

#### Scenario: Backend returns 403 Forbidden

- **WHEN** backend returns HTTP 403
- **THEN** transformed error includes `type: "permission_error"`

#### Scenario: Backend returns 404 Not Found

- **WHEN** backend returns HTTP 404
- **THEN** transformed error includes `type: "invalid_request_error"` with message indicating resource not found

#### Scenario: Backend returns 429 Rate Limited

- **WHEN** backend returns HTTP 429
- **THEN** transformed error includes `type: "rate_limit_error"`

#### Scenario: Backend returns 500 Server Error

- **WHEN** backend returns HTTP 500 or 502 or 503 or 504
- **THEN** transformed error includes `type: "upstream_error"`

### Requirement: Preserve Retry-After header

The system SHALL preserve rate limit headers in error responses.

#### Scenario: Backend returns rate limit with Retry-After

- **WHEN** backend returns HTTP 429 with `Retry-After` header
- **THEN** system includes `Retry-After` header in transformed error response
