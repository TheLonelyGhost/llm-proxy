# Configuration

`llm-proxy` is configured via [HCL2][hcl]. Configuration is resolved in this
order:

1. `-c` / `--config`
2. `LLM_PROXY_CONFIG`
3. `/etc/llm-proxy/config.hcl` on non-Windows, or `C:/ProgramData/llm-proxy/config.hcl` on Windows

The config path may point at either:

- a single `*.hcl` file, or
- a directory containing one or more `*.hcl` files (dot-files and non-`.hcl`
  files are ignored, subdirectories are not recursed).

Files in a directory are merged in lexicographic order. Conflicting attribute
definitions across files produce a load-time error rather than a silent
overwrite, so split your config however suits you.

## Top-level schema

```hcl
server {
  address = "127.0.0.1"   # interface to bind; default 127.0.0.1
  port    = 4980          # TCP port; default 4980
}

backend "<prefix>" {      # routing prefix; lowercase, no slashes
  type   = "<provider>"   # provider implementation; e.g. "github-copilot"
  models = ["..."]        # allow-list of upstream model ids (optional)
  # provider-specific attributes follow
}
```

You may declare any number of `backend` blocks. The block label (`"copilot"`
above) is the routing prefix clients use when picking a model
(`copilot/gpt-4o`). Labels must be unique, lowercase, and may not contain `/`.

### CLI overrides

`serve` accepts two flags that override file settings if (and only if)
explicitly passed:

- `--address <ip>` overrides `server.address`
- `--port <int>` overrides `server.port`

`-c` / `--config` is optional; if omitted, the proxy uses
`LLM_PROXY_CONFIG` and then the platform default path above.

Use `--log-level debug|info|warn|error` to control structured log verbosity.

## env() interpolation

A single HCL function is exposed: `env("NAME")` reads an environment variable
at load time. This is the canonical way to keep secrets out of source control:

```hcl
backend "copilot" {
  type           = "github-copilot"
  oauth_client_id = env("MY_GITHUB_OAUTH_CLIENT_ID")
}
```

`env()` returns the empty string when the variable is unset; the proxy treats
empty provider attributes as "use the default". File-reading helpers HCL
normally exposes (`file()`, `fileexists()`, etc.) are deliberately disabled to
reduce attack surface.

## On-disk state

The proxy is deliberately near-stateless. The only files it writes are:

| File                                                          | Purpose                                 | Permissions |
| ------------------------------------------------------------- | --------------------------------------- | ----------- |
| `<user-config-dir>/llm-proxy/github_token.json`               | Long-lived GitHub OAuth token           | 0600        |
| `<user-cache-dir>/llm-proxy/copilot_token.json`               | Short-lived (refreshed) Copilot token   | 0600        |

`<user-config-dir>` and `<user-cache-dir>` follow Go's `os.UserConfigDir` /
`os.UserCacheDir` semantics:

- **Linux:** `~/.config/llm-proxy/`, `~/.cache/llm-proxy/`
- **macOS:** `~/Library/Application Support/llm-proxy/`, `~/Library/Caches/llm-proxy/`
- **Windows:** `%AppData%\llm-proxy\`, `%LocalAppData%\llm-proxy\`

Both directories are created with mode 0700 on first use. `llm-proxy logout`
removes both files.

## Endpoints

The proxy exposes:

| Method | Path                       | Description                                |
| ------ | -------------------------- | ------------------------------------------ |
| GET    | `/healthz`                 | Liveness; returns registered backend names |
| GET    | `/v1/models`               | Aggregated, prefix-qualified model list    |
| POST   | `/v1/chat/completions`     | OpenAI chat completions (SSE supported)    |
| POST   | `/v1/completions`          | Legacy text completions                    |
| POST   | `/v1/embeddings`           | Embeddings                                 |

The client's `Authorization` header is ignored entirely; the proxy injects the
backend's own credentials. Request bodies are capped at 8 MiB.

## Routing

Every request must include a `model` field of the form
`"<backend-prefix>/<provider-model-id>"`. Examples:

| Client sends                 | Routed to        | Forwarded model       |
| ---------------------------- | ---------------- | --------------------- |
| `copilot/gpt-4o`             | `copilot`        | `gpt-4o`              |
| `copilot/claude-3.5`         | `copilot`        | `claude-3.5`          |
| `openai/openai/gpt-oss-120b` | `openai`         | `openai/gpt-oss-120b` |

Only the first `/` is significant: characters before it select the backend,
and everything after it (including any further `/` characters) is forwarded
verbatim as the upstream model id.

There is **no** default backend. A model id missing a prefix produces a 400.
A prefix with no matching backend produces a 404.

## Provider documentation

- [GitHub Copilot](providers/github-copilot.md)
- [IBM Bob](providers/ibm-bob.md)
- [OpenAI](providers/openai.md)

[hcl]: https://github.com/hashicorp/hcl
