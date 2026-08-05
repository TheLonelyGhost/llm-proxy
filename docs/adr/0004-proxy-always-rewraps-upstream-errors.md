# Proxy always re-wraps upstream error responses into the OpenAI error envelope

The Response Transform never passes upstream error responses through verbatim. Every upstream error (status ≥ 400) is re-encoded into the OpenAI error envelope shape (`{"error": {"message": ..., "type": ..., "code": ...}}`). The only upstream content preserved is the `message` string and select rate-limit headers (`Retry-After`, `X-RateLimit-Reset`).

This means the proxy is the authoritative source of the error envelope. Clients always receive a consistent, OpenAI-compatible error shape regardless of what the upstream sent.

## Consequences

- Upstream error details beyond `message` (e.g. provider-specific error codes, structured metadata) are silently dropped. If richer upstream diagnostics are needed in future, the transform layer must be extended explicitly.
- Clients cannot distinguish between a proxy-generated error and an upstream-generated one from the envelope shape alone — only the `type` field and HTTP status code signal origin.

## Considered Options

- **Pass upstream errors through verbatim** — rejected. Upstream error shapes vary across providers. Passing them through verbatim would expose provider-specific formats to clients, breaking the OpenAI-compatibility contract the proxy guarantees.
- **Pass through when upstream error is already OpenAI-shaped** — rejected. Detecting "already OpenAI-shaped" reliably is fragile and creates a two-path code flow that is harder to reason about. Uniform re-wrapping is simpler and safer.
