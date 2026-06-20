## Context

`copilot-responses-shim` already implements a complete OpenAI
`/v1/responses` ↔ `/chat/completions` translation layer in
`internal/providers/copilot/responses_shim.go`. The IBM Bob upstream also
serves a reliable `/chat/completions`, so the same translation is reusable
verbatim — only the dispatch (auth headers, HMAC signing, identity
resolution, retry, base URL) differs between backends.

The cleanest refactor lifts the translators into a shared package and lets
each provider supply its own dispatcher (already encapsulated in the
existing `forward` method).

## Goals / Non-Goals

**Goals:**
- One translator implementation shared by all backends that need a Responses
  shim.
- Bob preserves its existing chat-completions semantics (identity caching,
  HMAC, retry behaviour, modern-vs-legacy path selection) for
  `/v1/responses` traffic — the shim only changes the request/response
  *body shape*, not the transport.
- Tests for the shared package live with the package; per-provider tests
  cover the wiring (request reaches the right path with the right headers).

**Non-Goals:**
- No new translator features (no built-in tools, no
  `previous_response_id`, no retrieval). The Bob shim inherits the exact
  Copilot subset.
- Not addressing whether Bob's native `/v1/responses` is restored later. If
  it is, a follow-up change can route directly without the shim; the shim
  remains a portable fallback for any backend whose Responses surface is
  unreliable.
- No transform-layer changes — the `output`-pass-through path added in
  `copilot-responses-shim` already covers Bob.

## Decisions

### D1. Shared package `internal/responsesshim`

Lift the existing translators out of the Copilot package into
`internal/responsesshim`. The package has zero provider-specific knowledge:
it takes raw request bytes, returns rewritten bytes; takes upstream bytes,
returns translated bytes; takes an upstream SSE stream, returns a
translated SSE stream.

**Alternatives considered:**
- Leave the code in Copilot and import from Bob. Rejected — creates an
  inverted dependency (`ibmbob → copilot`) that future refactors will trip
  over.
- Put it under `internal/transform/`. Rejected — that package today is
  concerned with server-side response shaping (chat→responses chat-shape
  fallback, error envelopes); the shim is a different layer (provider-side
  request/response translation) and merging them blurs responsibilities.

### D2. Bob's dispatch path

Bob's `Responses` calls `responsesshim.TranslateRequest`, then routes the
resulting `chat` body through the same `forward` call as `Chat` does, using
`chatPathAuthn` on the authn backend and `chatPathLegacy` on the legacy
backend. All headers (Authorization, x-instance-id, x-team-id, signing) are
unchanged. On a 2xx response, the body is wrapped by
`responsesshim.TranslateChatStreamToResponses` (streaming) or run through
`TranslateChatCompletion` (non-streaming).

### D3. Identity + allow-list enforced before translation

`allowed(model)` and `ensureIdentity(ctx)` run before the request is
translated, so a disallowed model or a failed profile lookup short-circuits
without translating. This matches `Chat`.

### D4. Non-2xx upstream pass-through

If `forward` returns a response with status outside `[200, 300)`, Bob
returns it unchanged so the server's existing error-envelope handling
applies (502 / 504 / etc. with the upstream body). The shim is never
invoked on error bodies.

## Risks / Trade-offs

- **Risk**: Refactor breaks Copilot. → **Mitigation**: keep the public
  function signatures identical; rerun the full Copilot test suite
  (including the live e2e tests) after the move.
- **Risk**: Bob's HMAC signing depends on the request body bytes. If the
  shim translates the body, the bytes that get signed are the *translated*
  chat bytes — which is correct, because that's what the upstream is going
  to receive. → **Mitigation**: explicit test asserting the upstream `bob`
  fake receives a `/chat/completions` body (not a `/v1/responses` body)
  with valid signing headers (when applicable).
- **Trade-off**: Two backends now share a streaming SSE translator. Future
  changes to the event format (e.g. new OpenAI event types) affect both.
  Net: one place to fix bugs, one place to fix bugs in. Acceptable.

## Migration Plan

1. Land this change. `bob/<model>` immediately returns OpenAI Responses
   bodies instead of 501. Clients that previously fell back to chat
   completions on 501 will now receive a real response.
2. No data or config migration.
3. Rollback is a single revert.

## Open Questions

None blocking. If Bob's upstream `/v1/responses` becomes reliable later, we
can add a per-backend HCL toggle (`use_native_responses = true`) and route
directly. That is explicitly out of scope here.