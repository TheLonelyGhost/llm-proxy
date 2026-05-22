## Why

The previous change (`fix-responses-input-and-bob-forwarding`) settled on IBM
Bob returning HTTP 501 for `/v1/responses` because the upstream's native
`/v1/responses` surface is unreliable across deployments. With the Copilot
provider now shipping an in-proxy `/v1/responses` → `/chat/completions` shim
(`copilot-responses-shim`), we can apply the same approach to Bob: keep the
proxy's `/v1/responses` wire-compliant for clients while talking to Bob over
the reliable chat-completions surface.

This unblocks OpenAI Responses-API clients (OpenCode and SDKs) against
`bob/<model>` and produces a uniform contract across backends without
depending on Bob's upstream `/v1/responses` route.

**Supersedes** the `ibmbob-responses` capability spec in the
`fix-responses-input-and-bob-forwarding` change (which mandated a 501).

## What Changes

- Extract the existing Copilot shim translators from
  `internal/providers/copilot/responses_shim.go` into a new shared package
  `internal/responsesshim` so both Copilot and Bob (and any future backend)
  use one implementation.
- The shared package exposes three pure-function entrypoints:
  - `TranslateRequest(raw []byte) (chatBody []byte, model string, stream bool, err error)`
  - `TranslateChatCompletion(raw []byte, modelPrefix string) ([]byte, error)`
  - `TranslateChatStreamToResponses(body io.ReadCloser, modelPrefix string) io.ReadCloser`
- `copilot.Provider.Responses` switches to the shared package; behaviour and
  tests are unchanged.
- `ibmbob.Provider.Responses` replaces the `ErrUnsupported` stub with a real
  implementation that calls `responsesshim.TranslateRequest`, dispatches to
  the same upstream chat path as `Chat` (with identity caching, HMAC, retry,
  streaming preserved), and translates the response back. Allow-list and
  identity bootstrap are reused.
- Update `docs/providers/ibm-bob.md`: `/v1/responses` is now supported via
  the shim, with the same explicit non-goals as Copilot (no
  `previous_response_id`, no built-in tools, no retrieval). Update
  `docs/configuration.md` to remove the "bob returns 501" note.
- No new HCL configuration; no new state on disk.

## Capabilities

### New Capabilities
- `bob-responses-shim`: IBM Bob's `/v1/responses` translation layer
  (delegates to the shared shim, dispatches via the existing chat path,
  reuses identity/HMAC/auth).

### Modified Capabilities
- `ibmbob-responses` (from the in-flight `fix-responses-input-and-bob-forwarding`
  change): the 501 contract is superseded; that capability is replaced by
  `bob-responses-shim`. The replacement is described entirely under the new
  capability spec; once both changes are archived the older capability
  becomes a no-op delta.

## Impact

- **Code**:
  - `internal/responsesshim/shim.go` (new) — extracted translators.
  - `internal/responsesshim/shim_test.go` (new) — relocated unit tests.
  - `internal/providers/copilot/responses_shim.go` — deleted (replaced by
    thin call-site in `copilot.go`).
  - `internal/providers/copilot/responses_shim_test.go` — translator tests
    moved to the shared package; the copilot e2e tests stay where they are.
  - `internal/providers/ibmbob/ibmbob.go` — real `Responses` implementation
    using the shared shim and Bob's existing `forward`.
  - `internal/providers/ibmbob/provider_test.go` — replace
    `TestResponses_Unsupported` with translator-driven end-to-end tests
    (authn key path + legacy key path; non-streaming and streaming).
- **API surface**: `POST /v1/responses` against `bob/<model>` now returns
  OpenAI-compliant Responses bodies instead of HTTP 501. Existing clients
  using `/v1/chat/completions` against Bob are unaffected.
- **Docs**: `docs/providers/ibm-bob.md` and `docs/configuration.md`.
- **Dependencies**: none added.
- **State**: none added; endpoint remains stateless.
- **Breaking**: technically the shim returns 200 where the previous behavior
  was 501; clients that hard-coded the 501 fallback will now see a real
  response. This is the intended improvement.