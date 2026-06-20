## Context

The proxy is a thin OpenAI-API-compatible front door over pluggable backends
(`internal/backend.Backend`). For `/v1/chat/completions`, `/v1/completions`, and
`/v1/embeddings` the handler decodes only the `model` field, rewrites the request
body with the backend-stripped model id, and forwards the rest verbatim. The
`/v1/responses` handler in `internal/server/handlers.go:310-381` diverged from
this pattern: it decodes `input` into a Go `string` and bails out with HTTP 400
when the field is missing or fails to decode. Modern OpenAI clients send `input`
as an **array of input items**, which fails `json.Unmarshal` into a `string` and
produces a misleading "invalid JSON body" 400 against any backend, including
`bob/premium`.

The IBM Bob provider in `internal/providers/ibmbob/ibmbob.go:324-327` returns
`backend.ErrUnsupported` for `Responses`, which is mapped to HTTP 501. Bob's
upstream is LiteLLM-based, and LiteLLM natively serves `/v1/responses` with the
OpenAI schema (see https://docs.litellm.ai/docs/response_api), so the proxy can
forward instead of stubbing.

## Goals / Non-Goals

**Goals:**
- Make `/v1/responses` accept the full OpenAI request schema (string and array
  `input`, `reasoning.effort ∈ {minimal, low, medium, high}`).
- Forward `/v1/responses` from `bob/<model>` to the LiteLLM-compatible upstream,
  reusing the existing auth, HMAC signing, and streaming infrastructure of the
  chat path.
- Preserve the OpenAI error envelope for validation, unsupported, and upstream
  failures.

**Non-Goals:**
- Inventing response IDs, persisting response state, or implementing
  `/v1/responses/{id}` retrieval/cancellation/deletion on the proxy. The proxy
  remains stateless; if the upstream supports those routes, a follow-up change
  will add explicit pass-through.
- Translating between Responses and Chat Completions schemas. LiteLLM already
  bridges this internally, so a translation shim in the proxy would be redundant.
- Validating the inner structure of `input` array elements beyond "each element
  is a JSON object". The upstream owns full schema validation.

## Decisions

### D1. Carry `input` as `json.RawMessage`, not `string`

Change `backend.ResponsesRequest.Input` from `string` to `json.RawMessage`. The
handler validates "presence and is one of {string, array of objects}" and passes
the raw value through. Backends that need to inspect it can; backends that just
forward the rewritten body ignore it.

**Alternatives considered:**
- `any` / `interface{}` — loses byte-exact fidelity (Go re-marshals numbers as
  `float64`, mangling integer ids); rejected.
- Two fields (`InputString`, `InputItems`) — duplicates the contract and forces
  every backend to handle both; rejected.

### D2. Validate only "missing" and "wrong-kind", nothing deeper

The handler checks: (a) `input` key is present, (b) the value is JSON string or
JSON array, (c) if array, every element is a JSON object. Anything else returns
400 `invalid_request_error` with a precise message. Deep schema validation
(role/content/type) is delegated to the upstream — matching how the chat handler
trusts upstream validation today.

**Rationale:** keeps the proxy thin, avoids drifting from OpenAI's schema as it
evolves, and matches the existing architectural posture for other endpoints.

### D3. Reuse `Provider.forward` for Bob's `Responses` impl

Add two path constants — `responsesPathAuthn = "/inference/v1/responses"` and
`responsesPathLegacy = "/v1/responses"` — and dispatch identically to the chat
path. Streaming, HMAC, identity headers, and key-kind branching are unchanged.

**Alternatives considered:**
- A net-new HTTP client for `/v1/responses` — duplicates `forward` for no gain;
  rejected.
- A translation shim that maps Responses → chat completions in-proxy — LiteLLM
  already does this internally on the Bob upstream, so the shim is dead weight
  and creates a second source of truth; rejected.

### D4. Keep `reasoning.effort` validation, just widen the allow-list

Add `"minimal"` to the allowed set. Continue to reject unknown values with 400.

**Rationale:** the four values are stable OpenAI vocabulary; rejecting unknown
values early gives clients better error messages than a 502 from upstream.

### D5. Preserve existing transform pipeline

`transform.TransformResponsesResponse` already exists and is invoked for this
endpoint. No changes needed; the LiteLLM upstream already emits the OpenAI
Responses response shape, including SSE events for `stream=true`.

## Risks / Trade-offs

- **Risk**: A Bob upstream version older than LiteLLM 1.63.8 will 404 on
  `/v1/responses`. → **Mitigation**: surface upstream 404s as a 502 today (no
  regression vs. the prior 400 → 501), and document the minimum LiteLLM version
  in the Bob provider doc. We do not attempt local fallback to chat completions;
  that would silently degrade reasoning workloads.
- **Risk**: Changing `ResponsesRequest.Input` from `string` to `json.RawMessage`
  is a breaking change for any out-of-tree backend implementations. → **Mitigation**:
  no out-of-tree implementations are known; in-tree (copilot, ibmbob) are both
  updated in this change. Called out as **BREAKING** in the proposal.
- **Risk**: Larger `input` payloads (multimodal arrays) may hit the existing body
  size limit. → **Mitigation**: no change here; the existing
  `internal/server.readBody` limit already governs this and applies uniformly to
  other endpoints.

## Migration Plan

1. Land Part 1 (handler + contract): `/v1/responses` immediately accepts array
   `input`; `bob/<model>` now returns 501 (correct) instead of 400 (misleading).
2. Land Part 2 (Bob forwarding): `bob/<model>` returns real LiteLLM responses on
   `/v1/responses`.
3. Update `docs/providers/ibmbob.md` and `docs/configuration.md`.
4. No data migration; no config migration. Rollback is a single revert.

## Open Questions

- None blocking. Should Bob also forward `/v1/responses/{id}` GET/DELETE? Out of
  scope for this change; tracked separately if there is demand.
