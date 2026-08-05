# reasoning.effort is validated against the full OpenAI ReasoningEffort allow-list; reasoning_effort is passed through on chat

The `/v1/responses` handler validates `reasoning.effort` against the full set
defined by the OpenAI `ReasoningEffort` schema:

```
{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
```

Any other non-null value is rejected with HTTP 400 `invalid_request_error` naming
the accepted values. A null or absent `reasoning` object (or absent `effort`
field within it) is accepted and forwarded without validation.

The `/v1/chat/completions` handler does **not** validate `reasoning_effort` at
all. It forwards the field verbatim to the upstream along with the rest of the
request body. Validation is left to the upstream, consistent with the proxy's
thin-passthrough posture for chat.

These are two different fields on two different endpoints, and the proxy treats
them differently by design:

| Endpoint | Field | Proxy validates? |
|---|---|---|
| `POST /v1/responses` | `reasoning.effort` (inside object) | Yes — full allow-list |
| `POST /v1/chat/completions` | `reasoning_effort` (flat string) | No — passthrough |

## Consequences

- The proxy catches invalid effort values on `/v1/responses` before any upstream
  call, giving clients a clean 400 rather than a potentially confusing upstream
  error.
- As the OpenAI `ReasoningEffort` enum evolves (new values, removed values), the
  allow-list in `internal/server/handlers.go` must be updated to match
  `openai.openapi.json`.
- The chat path remains a thin passthrough; no effort-related validation is
  added there.

## Considered Options

- **Validate effort on the chat path too** — rejected. The chat path is a thin
  passthrough by design; adding validation there would couple the proxy to the
  upstream's model-specific effort support, which varies per model.
- **Use only `{"low", "medium", "high"}`** — rejected (was the initial bug).
  The live OpenAI spec includes `"none"`, `"minimal"`, `"xhigh"`, and `"max"`;
  blocking valid values causes the proxy to reject legitimate requests.
- **No validation on `/v1/responses` either** — rejected. Early validation
  produces better error messages; the effort field is stable vocabulary and the
  cost of maintaining the list is low.
