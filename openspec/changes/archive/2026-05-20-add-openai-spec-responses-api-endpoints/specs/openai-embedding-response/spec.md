## ADDED Requirements

### Requirement: Transform embedding response

The system SHALL transform backend embedding responses to match OpenAI's `/v1/embeddings` response schema.

#### Scenario: Successful embedding transformation

- **WHEN** backend returns HTTP 200 with embedding response body
- **THEN** system transforms response to include required fields: `object: "list"`, `data` array with objects containing `object: "embedding"`, `index`, `embedding` (float array), `model`, and `usage` object with `prompt_tokens` and `total_tokens`

#### Scenario: Multiple embeddings

- **WHEN** backend returns response with multiple embeddings for batch input
- **THEN** system transforms all embeddings preserving array order with correct `index` values starting from 0

#### Scenario: Backend response without usage data

- **WHEN** backend returns embedding response without `usage` field
- **THEN** transformed response includes `usage: {"prompt_tokens": 0, "total_tokens": 0}`

### Requirement: Preserve model routing prefix in embeddings

The system SHALL preserve the original prefixed model name in transformed embedding responses.

#### Scenario: Embedding with prefixed model

- **WHEN** client requests model `openai/text-embedding-3-small` and backend returns response with model `text-embedding-3-small`
- **THEN** transformed response SHALL include model field as `openai/text-embedding-3-small`

### Requirement: Validate embedding vector dimensions

The system SHALL validate that embedding vectors are non-empty float arrays.

#### Scenario: Backend returns empty embedding vector

- **WHEN** backend returns embedding response with empty `embedding` array
- **THEN** system returns HTTP 502 with OpenAI-format error: `{"error": {"message": "upstream returned invalid embedding: empty vector", "type": "upstream_error", "code": null}}`

#### Scenario: Backend returns non-numeric embedding values

- **WHEN** backend returns embedding with non-numeric values in vector
- **THEN** system returns HTTP 502 with OpenAI-format error describing invalid vector data
