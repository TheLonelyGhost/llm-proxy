# /v1/responses error mapping: ErrUnsupported→501, ErrUnknownModel→404, other→502

The `/v1/responses` handler maps backend errors to HTTP status codes as follows:

| Condition | HTTP status | Error type in envelope |
|---|---|---|
| `errors.Is(err, backend.ErrUnsupported)` | 501 | `not_implemented` |
| `errors.Is(err, backend.ErrUnknownModel)` | 404 | `invalid_request_error` |
| Any other non-nil error | 502 | `upstream_error` |

This mapping is identical to the error behaviour documented in ADR-0004 for
other endpoints, extended with the 501 case that is specific to optional
backend methods. All error bodies use the OpenAI envelope:
`{"error": {"message": "...", "type": "...", "code": null}}`.

The 501 path is the mechanism by which a backend signals that it does not
implement the Responses API at all. Backends that implement the Responses API
via the shim (Copilot, IBM Bob) never return `ErrUnsupported`; only backends
whose `Responses` method is a stub stub do.

## Consequences

- Clients receive a deterministic, OpenAI-shaped error for every failure mode.
- Clients can distinguish "this backend does not support Responses" (501) from
  "this model is not available" (404) from "the upstream failed" (502).
- The 501 path provides a clean fallback signal: clients that cannot use the
  Responses API can fall back to `/v1/chat/completions` on 501.

## Considered Options

- **Return 400 for unsupported** — rejected (was the original bug). 400
  implies a client mistake; 501 correctly indicates the server does not
  implement the method.
- **Vary the error type by backend** — rejected. Uniform envelopes are
  simpler for clients and consistent with ADR-0004.
