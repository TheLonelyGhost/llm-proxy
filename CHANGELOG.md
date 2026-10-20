# Changelog

All notable end-user facing changes to `llm-proxy` are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
This project uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

### Added

- Plugin sidecars started by `llm-proxy serve` now receive `LLM_PROXY_BACKEND_LABEL`, `LLM_PROXY_BACKEND_TYPE`, and `LLM_PROXY_BACKEND_CONFIG`, so a plugin can read its `backend` block attributes at serve time and not only in `plugin run`.

### Fixed

- Pipeline no longer uses SLSA releaser, allowing multiarch builds to be uploaded to GitHub Releases
- List, map, and object attributes in `LLM_PROXY_BACKEND_CONFIG` are now encoded as JSON arrays and objects instead of Go-syntax strings.
- Responses from plugin sidecars are no longer cut off after 60 seconds; long streams and slow generations now run until they complete or the client disconnects.

## [1.0.0] — 2026-08-10

### Removed

- **BREAKING:** `llm-proxy providers` subcommand group removed, including `llm-proxy providers list-models`. Upstream model discovery is now a plugin concern. Plugins that support it expose a `list-models` tool; replace any `llm-proxy providers list-models <backend>` calls with `llm-proxy plugin run <backend-label> list-models`. The tool is not filtered by the operator allow-list, preserving the same discovery behaviour. See `docs/plugins/authoring.md` for the plugin-author contract.
- **BREAKING:** `llm-proxy login` and `llm-proxy logout` top-level commands removed. Authentication is entirely a plugin concern. Plugins that require interactive credential setup must expose a `login` tool; invoke it with `llm-proxy plugin run <backend-label> login`. Same with `logout` tool, invoked with `llm-proxy plugin run <backend-label> logout`.
- **BREAKING:** `github-copilot` backend removed from core; it is now the external plugin [`github.com/thelonelyghost/llm-proxy-provider-copilot`](https://github.com/thelonelyghost/llm-proxy-provider-copilot). Add a `plugin` block pointing to that module and update your `backend` blocks accordingly.
- **BREAKING:** `ibm-bob` backend removed from core; it is now the external plugin [`github.com/thelonelyghost/llm-proxy-provider-ibmbob`](https://github.com/thelonelyghost/llm-proxy-provider-ibmbob). Add a `plugin` block pointing to that module and update your `backend` blocks accordingly.

### Added

- `GET /v1/usage/budget` now supports sidecar plugin backends: if a plugin registers a `budget` tool in its manifest, the proxy runs it with `--tool budget`, parses its stdout as JSON (per `assets/budget.schema.json`), and includes the result in the response keyed by the backend label. The `BudgetReporter` Go interface path for compiled-in providers is preserved unchanged.
- Plugin `version` field now accepts Terraform-style version constraints (`~>`, `>=`, `<`, etc.); `llm-proxy plugin install` queries the GitHub Releases API and selects the highest matching release when the source is `github.com`.
- `llm-proxy plugin install` now writes `llm-proxy.lock.hcl` recording the resolved concrete version per plugin per platform. On subsequent runs the lock file is honoured (no network call), the local cache is checked before any download, and already-cached versions are skipped.
- `llm-proxy plugin install --upgrade` bypasses the lock file and local cache during version resolution, always selecting the latest compatible version from the remote registry and updating the lock file. Binary download is still skipped when the resolved version is already cached.
- `llm-proxy plugin install` now downloads the `SHA256SUMS` file alongside each plugin archive (auto-derived for `github.com` sources; configurable via `checksum_url` on the `plugin` block for custom release servers) and verifies the archive's SHA-256 before extraction. The verified checksum is recorded in `llm-proxy.lock.hcl`. `llm-proxy serve` checks the on-disk binary against the lock file checksum at startup and hard-fails if it has been modified.
- IBM Bob browser SSO via `llm-proxy ibm-bob login`; tokens are cached and refreshed automatically.
- Model catalogue cache (in-memory + on-disk, 24 h TTL); stale data served when upstream is unreachable.
- Upstream request/response body logging at DEBUG when `server.log_bodies = true`.
- `reasoning.effort` values `none`, `xhigh`, and `max` on `/v1/responses`.

### Fixed

- `serve` now uses the lock-file-resolved concrete version (e.g. `1.2.3`) when locating the plugin binary in the cache, instead of the raw version constraint string (e.g. `~> 1.0`). Previously, a plugin declared with a range constraint would never be found after install and `serve` would always report the binary as missing.
- `tool_calls` arrays were dropped from chat completion responses.
- `finish_reason="tool_calls"` without a `tool_calls` array is now normalised to `"stop"`.
- `/v1/responses` is no longer marked experimental.

## [0.1.0] — 2026-05-22

Initial public release.

### Added

- Three built-in backends: `github-copilot` (OAuth device flow), `ibm-bob` (API key + HMAC), `openai` (passthrough).
- Prefix-based routing: clients use `<backend>/<model-id>` (e.g. `copilot/gpt-4o`).
- OpenAI-compatible endpoints: `GET /healthz`, `GET /v1/models`, `GET /v1/model/info`, `POST /v1/chat/completions`, `POST /v1/completions`, `POST /v1/embeddings`, `POST /v1/responses`.
- `GET /v1/usage/budget` — per-backend spend/quota (IBM Bob); `?backend=<prefix>` for single-backend query.
- `/v1/model/info` — capability flags, token limits, reasoning effort levels per model.
- OpenTelemetry tracing (`server { tracing = true }` or `--trace`).
- HCL2 config: single file or merged directory; `env("NAME")` interpolation.
- `llm-proxy providers list-models <backend>` (`--verbose`, `--json`).
- `llm-proxy login` / `llm-proxy logout` for GitHub Copilot OAuth.
- Model Allow-List via `models = [...]` in a `backend` block.
- Secure defaults: binds `127.0.0.1:4980`, ignores client `Authorization`, 8 MiB body cap, 0600 credentials.
- `server.log_bodies` — opt-in DEBUG body logging (request + non-streaming responses, 64 KiB cap).
- XDG-compliant dirs on Linux; `LLM_PROXY_CONFIG_DIR` / `LLM_PROXY_CACHE_DIR` override on all platforms.
- SLSA level 3 release provenance via GitHub Actions.
- Cross-platform CGO-free builds for `linux`, `darwin`, `windows` × `amd64`/`arm64`.

[Unreleased]: https://github.com/thelonelyghost/llm-proxy/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/thelonelyghost/llm-proxy/compare/v0.1.0...v1.0.0
[0.1.0]: https://github.com/thelonelyghost/llm-proxy/releases/tag/v0.1.0
