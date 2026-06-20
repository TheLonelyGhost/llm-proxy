## Why

The `/v1/responses` endpoint returns HTTP 400 for every modern client that targets
`bob/premium` (and any other backend), because the proxy's request handler decodes
`input` as a Go `string` and rejects the array form emitted by the official OpenAI
SDKs. The status code is also misleading: the IBM Bob backend stub silently
short-circuits with `ErrUnsupported`, but clients never reach that path because
the validator rejects their input shape first.

## What Changes

- Accept both forms of OpenAI's `input` field in `/v1/responses`: a plain string
  **or** an array of input items. Reject only when truly missing or when the value
  is neither a string nor an array of objects.
- Extend the `reasoning.effort` allow-list to include `"minimal"` (GPT-5 family),
  matching the current OpenAI spec.
- Change the internal `backend.ResponsesRequest.Input` contract from `string` to
  `json.RawMessage` so backends can see the original shape. **BREAKING** for any
  out-of-tree backend implementations of `backend.Backend.Responses`.
- IBM Bob continues to return `backend.ErrUnsupported` for `/v1/responses`,
  which the server now correctly surfaces as HTTP 501 `not_implemented` once
  the input-shape validator stops swallowing requests with 400. We do **not**
  forward `/v1/responses` to Bob's LiteLLM-compatible upstream: that surface is
  not reliably available across Bob deployments, and a silent translation to
  `/v1/chat/completions` would hide reasoning-model behaviour differences.
- Preserve the existing OpenAI-shaped error envelope: validation errors are 400
  `invalid_request_error`; unsupported endpoints are 501 `not_implemented`;
  upstream errors are 502 `upstream_error`.

## Capabilities

### New Capabilities
- `responses-endpoint`: the proxy's `/v1/responses` ingress contract (accepted
  request shapes, validation, error envelope, transformation, and dispatch to a
  backend).
- `ibmbob-responses`: the IBM Bob provider's `/v1/responses` behavior (returns
  `ErrUnsupported`, which the server maps to HTTP 501).

### Modified Capabilities
<!-- None: no existing specs in openspec/specs/. -->

## Impact

- **Code**: `internal/server/handlers.go` (handler rewrite), `internal/backend/backend.go`
  (`ResponsesRequest.Input` type change), and tests in
  `internal/server/server_test.go` and `internal/providers/ibmbob/`.
- **API surface**: `/v1/responses` now accepts the full OpenAI request schema.
  No change to existing string-form callers. New `"minimal"` value accepted on
  `reasoning.effort`. `bob/<model>` returns 501 `not_implemented` (was 400
  `invalid JSON body`).
- **Docs**: `docs/configuration.md` notes the supported `input` shapes and
  effort values. `docs/providers/ibm-bob.md` documents that `/v1/responses` is
  unsupported and clients should use `/v1/chat/completions`.
- **Dependencies**: none added.
- **State**: none added; the endpoint remains stateless.
