# AGENTS.md

Operating instructions for AI coding agents working in this repository.

## Project at a glance

- Go module: `github.com/thelonelyghost/llm-proxy`
- Binary: `llm-proxy` (entry point at `cmd/llm-proxy`)
- Build constraint: **CGO disabled** (`CGO_ENABLED=0`); enforced by `Taskfile.yml`.
- Target platforms: Linux, macOS, Windows × amd64, arm64.
- License: MIT.

## Required tooling

Install once with `task tools:install` (installs `nilaway`, `govulncheck`).
Other tools you must have on PATH:

- `go` (version pinned in `go.mod`)
- `task` (https://taskfile.dev)
- `golangci-lint`

## Workflow rules

1. **Never commit code that fails any of these:**
   ```
   task fmt
   task vet
   task lint
   task nilaway
   task vuln
   task test:cover
   ```
   Run `task check` to do all of the above in one go. **ALWAYS RUN `task check`
   before considering an implmementation complete.**

2. **Coverage threshold is 80%.** `task test:cover` enforces this.

3. **Add tests for every change.** Public interfaces require unit tests; the
   HCL loader and the model id parser have fuzz targets, and any new
   parsing/decoding logic should grow one too. Run `task fuzz` to exercise
   them briefly.

4. **Documentation must move with the code.** If you change the HCL schema,
   update `docs/configuration.md`. If you add a backend, add a page under
   `docs/providers/`. If you change CLI flags or top-level behaviour, update
   `README.md`.

5. **Do not weaken secure defaults.** The proxy binds to `127.0.0.1`, ignores
   client `Authorization` headers, refuses to silently default a backend, and
   writes secrets with mode 0600 via `paths.WriteSecret`. Match these patterns
   in any new code.

6. **Stateless by default.** Persist on the filesystem only when there is no
   reasonable alternative (e.g., OAuth refresh tokens). Use
   `internal/paths.ConfigDir()` for long-lived credentials and
   `internal/paths.CacheDir()` for short-lived caches.

## Architecture

The proxy is a thin front door over a pluggable `backend.Backend` interface.

```
HTTP request
  -> internal/server   (mux, body limits, error envelope, hop-by-hop stripping)
  -> internal/router   (parses "<prefix>/<model>")
  -> backend.Registry  (lookup by prefix)
  -> backend.Backend   (one impl per provider)
       chat / complete / embed
```

To add a new backend:

1. Create `internal/providers/<name>/` implementing `backend.Backend`.
2. Decode any provider-specific HCL via `config.DecodeBackend(block, &settings)`.
3. Register the provider type in `internal/cli/serve.go`'s
   `registerBackends` switch.
4. Add a `docs/providers/<name>.md` page.
5. Add unit tests using `httptest.Server` for upstream behaviour (see
   `internal/providers/copilot/copilot_test.go` or
   `internal/providers/ibmbob/provider_test.go`).

## Style

- Code is formatted with `gofmt -s` and `goimports`.
- Public symbols carry doc comments starting with the symbol name.
- Logging uses `log/slog`. Never log request/response bodies or token values.
- Errors are wrapped with `%w`; sentinel errors live next to the type or in
  the package that produces them (e.g., `backend.ErrUnsupported`).

## Common tasks

| Want to...                          | Run                                       |
| ----------------------------------- | ----------------------------------------- |
| Compile for current host            | `task build`                              |
| Cross-compile every supported tuple | `task build:all`                          |
| Run unit tests                      | `task test`                               |
| Check coverage threshold            | `task test:cover`                         |
| Run fuzz targets briefly            | `task fuzz`                               |
| Lint / vet / vulncheck / nilaway    | `task lint`, `task vet`, `task vuln`, `task nilaway` |
| Full pre-commit pipeline            | `task check`                              |
| Run locally against examples/       | `task run`                                |

## When in doubt

Ask for clarification before guessing - especially before introducing new
dependencies, expanding the HCL schema, or persisting new on-disk state.
