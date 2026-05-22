## 1. Backend contract

- [ ] 1.1 Change `backend.ResponsesRequest.Input` from `string` to
  `json.RawMessage` in `internal/backend/backend.go`.
- [ ] 1.2 Update doc comment on `ResponsesRequest.Input` to note that it carries
  either the JSON string or JSON array form, byte-exact from the client.

## 2. Server handler (`/v1/responses`)

- [ ] 2.1 Rewrite `handleResponses` in `internal/server/handlers.go` to decode
  the body twice: once into a typed struct that captures `model`, `stream`,
  `reasoning`, and a `json.RawMessage` `input`; once into a `map[string]any` for
  the model-rewrite path that already exists.
- [ ] 2.2 Validate `input` presence: reject with HTTP 400 `invalid_request_error`
  when the raw value is absent, JSON `null`, or an empty JSON string.
- [ ] 2.3 Validate `input` kind: accept only JSON string or JSON array whose
  elements are JSON objects; reject other kinds with HTTP 400 and a precise
  message naming the accepted shapes.
- [ ] 2.4 Extend the `reasoning.effort` allow-list to
  `{"minimal", "low", "medium", "high"}`; keep the existing 400 envelope.
- [ ] 2.5 Pass the raw `input` through in `backend.ResponsesRequest.Input` and
  keep the existing rewritten-body forwarding path intact.

## 3. IBM Bob backend (Part 2)

- [ ] 3.1 Add `responsesPathAuthn = "/inference/v1/responses"` and
  `responsesPathLegacy = "/v1/responses"` constants alongside the existing chat
  path constants in `internal/providers/ibmbob/ibmbob.go`.
- [ ] 3.2 Replace the `Responses` stub at `ibmbob.go:324-327` with a real
  implementation that mirrors `Chat`: enforce `allowed(model)`, call
  `ensureIdentity`, choose the path by `authnBackend`, and delegate to
  `p.forward(ctx, http.MethodPost, path, req.Body, req.Stream)`.
- [ ] 3.3 Confirm `forward` already sets HMAC, identity, and streaming headers
  appropriately for this path (no provider-specific allow-list); adjust only if
  a gap is found.

## 4. Tests

- [ ] 4.1 In `internal/server/server_test.go`, add a happy-path table case for
  `/v1/responses` with `input` as an array of items; assert non-400 and that the
  backend received a rewritten body whose `input` value is byte-equal to the
  original.
- [ ] 4.2 Add a case for `reasoning.effort: "minimal"` accepted; keep the
  existing invalid-effort 400 test.
- [ ] 4.3 Add cases for the new 400 messages: missing `input`, `input: null`,
  `input: 42`, `input: [1,2]` (array of non-objects).
- [ ] 4.4 In `internal/providers/ibmbob/`, add a `Responses_test.go` using
  `httptest.Server` that asserts:
  - authn key kind hits `/inference/v1/responses`;
  - legacy key kind hits `/v1/responses`;
  - request body is forwarded verbatim;
  - streaming flag is propagated;
  - model not in the allow-list returns `ErrUnknownModel` without contacting the
    upstream.
- [ ] 4.5 Extend the existing fuzz target for the request decoder (or add one
  for `/v1/responses` if none exists) so the new validator is fuzzed.

## 5. Docs

- [ ] 5.1 Update `docs/configuration.md` to note that `/v1/responses` accepts
  both `input` shapes and the four `reasoning.effort` values.
- [ ] 5.2 Update `docs/providers/ibmbob.md` (or create it if absent) to document
  `/v1/responses` support and the minimum LiteLLM version on the upstream.

## 6. Verification

- [ ] 6.1 Run `task fmt && task vet && task lint && task nilaway && task vuln`.
- [ ] 6.2 Run `task test:cover` and confirm coverage stays >= 80%.
- [ ] 6.3 Run `task fuzz` briefly to exercise the new fuzz cases.
- [ ] 6.4 Run `task check` end-to-end before declaring done.
