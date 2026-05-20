## ADDED Requirements

### Requirement: Transform streaming chat completion events

The system SHALL transform backend SSE streaming responses to match OpenAI's SSE format with `data: {...}\n\n` lines.

#### Scenario: Backend streams chat completion chunks

- **WHEN** backend streams chat completion with `Stream: true`
- **THEN** each SSE event SHALL be formatted as `data: <json>\n\n` where JSON contains `id`, `object: "chat.completion.chunk"`, `created`, `model`, `choices` with `delta` object

#### Scenario: Backend sends final stream event

- **WHEN** backend sends final event indicating stream end
- **THEN** system transforms to `data: [DONE]\n\n`

#### Scenario: Backend sends empty delta

- **WHEN** backend streams chunk with no content delta
- **THEN** system transforms to valid OpenAI chunk with empty `delta` object

### Requirement: Transform streaming text completion events

The system SHALL transform backend streaming text completion responses to OpenAI's SSE format.

#### Scenario: Backend streams text completion tokens

- **WHEN** backend streams text completion with `Stream: true`
- **THEN** each SSE event SHALL be formatted as `data: <json>\n\n` where JSON contains `id`, `object: "text_completion"`, `created`, `model`, `choices` with `text` field

### Requirement: Transform streaming responses endpoint events

The system SHALL transform backend streaming responses from `/v1/responses` endpoint to OpenAI's SSE format.

#### Scenario: Backend streams response chunks

- **WHEN** backend streams response from `/v1/responses` with `Stream: true`
- **THEN** each SSE event SHALL be formatted as `data: <json>\n\n` where JSON contains `id`, `object: "response.chunk"`, `created`, `model`, `choices` with `delta` object

#### Scenario: Streaming response with reasoning deltas

- **WHEN** backend streams response chunks containing reasoning output deltas
- **THEN** each chunk includes reasoning delta fields in choice object with proper formatting

### Requirement: Handle streaming errors

The system SHALL handle errors during streaming without corrupting the SSE stream.

#### Scenario: Backend closes stream prematurely

- **WHEN** backend closes stream before sending `[DONE]`
- **THEN** system sends error event: `data: {"error": {"message": "upstream closed stream prematurely", "type": "upstream_error", "code": null}}\n\n` then closes connection

#### Scenario: Backend sends malformed SSE event

- **WHEN** backend sends SSE event with invalid JSON
- **THEN** system logs error and omits the malformed event from client stream

### Requirement: Preserve SSE keep-alive comments

The system SHALL preserve SSE comment lines for connection keep-alive.

#### Scenario: Backend sends SSE comments

- **WHEN** backend sends lines starting with `:` (SSE comments)
- **THEN** system passes those lines through unchanged to maintain connection keep-alive

### Requirement: Incremental streaming without buffering

The system SHALL transform streaming responses incrementally without buffering the entire response.

#### Scenario: Large streaming response

- **WHEN** backend streams response larger than 1MB
- **THEN** system transforms and forwards each event immediately without accumulating response in memory

#### Scenario: Streaming with slow client

- **WHEN** client reads stream slower than backend produces events
- **THEN** system applies backpressure to backend and does not buffer unbounded events

### Requirement: Preserve model prefix in streaming responses

The system SHALL preserve the original prefixed model name in all streaming events.

#### Scenario: Streaming chat with prefixed model

- **WHEN** client requests streaming model `copilot/gpt-4o` and backend streams chunks with model `gpt-4o`
- **THEN** each transformed chunk SHALL include model field as `copilot/gpt-4o`
