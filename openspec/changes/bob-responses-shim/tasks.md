## 1. Extract shared shim package

- [ ] 1.1 Create `internal/responsesshim/shim.go` with `TranslateRequest`,
  `TranslateChatCompletion`, and `TranslateChatStreamToResponses` lifted
  verbatim from `internal/providers/copilot/responses_shim.go` (only the
  symbol case changes from unexported to exported).
- [ ] 1.2 Move the translator unit tests to
  `internal/responsesshim/shim_test.go`. The Copilot package's
  responses_shim_test.go is removed; the e2e tests in `copilot_test.go`
  stay where they are.
- [ ] 1.3 Delete `internal/providers/copilot/responses_shim.go` and update
  `copilot.go` to call the shared functions.

## 2. Wire Bob

- [ ] 2.1 Replace the `ErrUnsupported` stub at
  `internal/providers/ibmbob/ibmbob.go::Responses` with: `allowed(model)` →
  `ensureIdentity(ctx)` → `responsesshim.TranslateRequest(req.Body)` →
  `forward("POST", chatPath, chatBody, stream)` (choose path the same way
  `Chat` does) → on 2xx, wrap or convert via the shim; otherwise
  pass-through.
- [ ] 2.2 Set `Content-Type: text/event-stream` and `Cache-Control: no-cache`
  on streaming responses; `application/json` on non-streaming.

## 3. Tests

- [ ] 3.1 Replace `TestResponses_Unsupported` in
  `internal/providers/ibmbob/provider_test.go` with:
  - `TestResponses_AuthnBackend_NonStreaming`: drives a JWT/HMAC config,
    asserts the fake upstream received `/inference/v1/chat/completions`
    with valid signing headers and a translated body; asserts the client
    sees Responses-shape JSON.
  - `TestResponses_LegacyBackend_NonStreaming`: legacy key, asserts the
    upstream path is `/chat/completions` and no `/admin/v1/profile` call
    happens.
  - `TestResponses_AuthnBackend_Streaming`: same as the first but
    `stream: true`; asserts the client sees the ordered named-event
    sequence and the upstream got a chat-completions body.
  - `TestResponses_AllowListEnforced` (kept): model outside the allow-list
    returns `ErrUnknownModel` without contacting the upstream.
- [ ] 3.2 Re-add the `responsesCalls` / `responsesPath` fields on the
  ibmbob fake backend; the upstream handler is now `handleChat` for
  responses traffic too (same path), but the test should verify the body
  is `messages`-shaped not `input`-shaped.

## 4. Docs

- [ ] 4.1 `docs/providers/ibm-bob.md` — replace the "/v1/responses is not
  supported" note with a "Responses API shim" section that mirrors the
  Copilot doc (supported subset, function-tool round-trip, explicit
  non-goals, id derivation).
- [ ] 4.2 `docs/configuration.md` — remove any text claiming Bob returns
  501 on `/v1/responses`.

## 5. Verification

- [ ] 5.1 `task fmt && task vet && task lint && task nilaway && task vuln`.
- [ ] 5.2 `task test:cover` — coverage stays ≥ 80%.
- [ ] 5.3 `task check` end-to-end.
- [ ] 5.4 Optional live smoke against a real Bob backend if credentials are
  available locally (mirrors the Copilot smoke from the previous change).