# Response transform layer sits at the server, not the backend

All upstream HTTP responses pass through a dedicated `internal/transform` package
before being written to the client. The transform layer applies per-endpoint
normalisations (field mapping, model-prefix rewriting, usage defaulting) and
re-encodes the result as a well-formed OpenAI-compliant body. Streaming responses
are transformed incrementally — event by event — using a `bufio.Scanner`-based
SSE parser; the full response body is never buffered.

There is one transformer function per endpoint family:

- `TransformChatResponse` — `/v1/chat/completions`
- `TransformCompletionResponse` — `/v1/completions`
- `TransformEmbeddingResponse` — `/v1/embeddings`
- `TransformResponsesResponse` — `/v1/responses`

Each function handles both the streaming and non-streaming branches for its
endpoint. Error responses (status ≥ 400) are never passed through verbatim; they
are always re-encoded into the OpenAI error envelope (see ADR-0004).

If a backend returns valid HTTP 2xx but with a body that fails to decode (invalid
JSON, missing required fields), the transform layer returns HTTP 502 with an
`upstream_error` envelope rather than forwarding the malformed bytes.

## Consequences

- Backends need no knowledge of OpenAI response shapes. They return a raw
  `*backend.Response` and the server handles all normalisation.
- Malformed upstream responses surface as 502 to the client rather than
  silently corrupt data.
- SSE events larger than the scanner's token limit (64 KiB) are dropped
  with a DEBUG log; the stream is not closed.

## Considered Options

- **Transformation inside each backend** — rejected. Would require each of
  the three (or more) backends to duplicate the same normalisation logic.
- **Single generic transformer** — rejected. Chat, completions, embeddings,
  and responses have structurally different schemas; a single function would
  need fragile type-detection and would be harder to test and maintain.
- **Middleware wrapper around the backend interface** — rejected. Harder to
  debug streaming issues, and the abstraction boundary is less clear than a
  dedicated package called explicitly by the server handler.
