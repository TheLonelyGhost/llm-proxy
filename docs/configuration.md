# Configuration

## Quick start

The minimal config to get `llm-proxy` running is a single backend block. Save this
as `~/.config/llm-proxy/config.hcl` (or pass `-c <path>`):

```hcl
# Use the built-in OpenAI-compatible backend
backend "openai" {
  type    = "openai"
  api_key = env("OPENAI_API_KEY")
  models  = ["gpt-4o", "gpt-4o-mini"]
}
```

Then start the proxy:

```sh
llm-proxy serve
```

Point any OpenAI-compatible client at `http://127.0.0.1:4980` and prefix your model
name with the backend label:

```sh
curl http://127.0.0.1:4980/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"openai/gpt-4o","messages":[{"role":"user","content":"hi"}]}'
```

That's it. The sections below explain the details.

---

## Config file location

Configuration is resolved in this order (first match wins):

1. `-c` / `--config` flag
2. `LLM_PROXY_CONFIG` environment variable
3. `~/.config/llm-proxy/config.hcl` on Linux/macOS, `C:\ProgramData\llm-proxy\config.hcl`
   on Windows

The path may point at either a single `*.hcl` file or a **directory**. When a directory
is given, every `*.hcl` file inside it (no subdirectories, no dot-files) is loaded
and merged in lexicographic order. Conflicting attribute definitions across files
produce a load-time error.

## Keeping secrets out of config

Use `env("NAME")` to read an environment variable at load time:

```hcl
backend "openai" {
  type    = "openai"
  api_key = env("OPENAI_API_KEY")
}
```

`env()` returns the empty string when the variable is unset; the proxy treats an
empty provider attribute as "use the built-in default". Never hardcode API keys
-- commit only the `env()` references.

> **Note:** File-reading helpers that HCL normally exposes (`file()`, `fileexists()`,
> etc.) are deliberately disabled to reduce attack surface.

## Backends

A `backend` block wires a routing prefix to a provider:

```hcl
backend "<prefix>" {
  type   = "<provider>"   # e.g. "openai"
  models = ["..."]        # optional allow-list of upstream model ids

  # provider-specific attributes (see docs/providers/)
}
```

- **`<prefix>`** is the label clients use when selecting a model: `<prefix>/<model-id>`
  (e.g. `openai/gpt-4o`).
- **`type`** is either a compiled-in provider name or the label of a `plugin` block.
- **`models`** is an optional allow-list. Requests for unlisted models are rejected
  with 404. Omitting it means no model is advertised via `/v1/models`, but routing
  still works.

You may declare any number of `backend` blocks. Labels must be unique, lowercase,
and must not contain `/`.

See [docs/providers/](providers/) for provider-specific attributes.

## Routing

Every request must include a `model` field of the form
`"<backend-prefix>/<provider-model-id>"`:

| Client sends                     | Routed to        | Forwarded model       |
| -------------------------------- | ---------------- | --------------------- |
| `openai/gpt-4o`                  | `openai`         | `gpt-4o`              |
| `copilot/gpt-4o`                 | `copilot`        | `gpt-4o`              |
| `openrouter/openai/gpt-oss-120b` | `openrouter`     | `openai/gpt-oss-120b` |

Only the first `/` is significant — everything after it (including further `/` characters)
is forwarded verbatim as the upstream model id.

There is **no default backend.** A model id missing a prefix returns HTTP 400; an
unknown prefix returns HTTP 404.

## Endpoints

See [docs/endpoints.md](endpoints.md) for the full endpoint reference, including
model capability discovery, reasoning effort levels, the responses endpoint input
shapes, and budget / quota reporting.

## CLI flags

`serve` accepts two flags that override file settings:

- `--address <ip>` overrides `server.address`
- `--port <int>` overrides `server.port`
- `--log-level debug|info|warn|error` controls structured log verbosity

## Provider documentation

- [GitHub Copilot](providers/github-copilot.md)
- [IBM Bob](providers/ibm-bob.md)
- [OpenAI](providers/openai.md)

---

## Advanced usage

### Full server block schema

```hcl
server {
  address = "127.0.0.1"   # interface to bind; default 127.0.0.1
  port    = 4980          # TCP port; default 4980

  # Graceful shutdown timeout. Default 30s.
  shutdown_timeout = "30s"

  # Tracing (opt-in). See "Tracing" below.
  tracing        = false
  trace_exporter = "stdout"
  trace_sampler  = "always_on"
  trace_ratio    = 1.0
  trace_service  = "llm-proxy"

  # DEBUG-level body logging. Default false.
  # SECURITY: see "Body logging" below before enabling.
  log_bodies = false
}
```

### Plugins

Plugins extend the proxy with out-of-process provider backends. See [docs/plugins/index.md](plugins/index.md)
for the full plugin reference, including the `plugin` block schema, source address
formats, version constraints, cache layout, and lifecycle management.

For the lock file format, resolution priority, per-platform entries, and upgrade
behaviour, see [docs/lockfile.md](lockfile.md).

### Tracing

Enable OpenTelemetry tracing via the `server` block or CLI:

```hcl
server {
  tracing        = true
  trace_exporter = "stdout"   # currently only "stdout" is supported
  trace_sampler  = "always_on"
  trace_ratio    = 1.0        # only used for ratio-based samplers
  trace_service  = "llm-proxy"
}
```

Or pass `--trace` to `serve` to enable with defaults.

Sampling modes:

| Mode                       | Behaviour                                    |
| -------------------------- | -------------------------------------------- |
| `always_on`                | Sample every span                            |
| `always_off`               | Sample nothing                               |
| `ratio`                    | Sample `trace_ratio` fraction of spans       |
| `parent_based_always_on`   | Honour parent span's decision; default on    |
| `parent_based_ratio`       | Honour parent span's decision; default ratio |

### Body logging (DEBUG)

`server.log_bodies` (default `false`) emits a DEBUG-level line containing the HTTP
request body and response body.

**Both** `server.log_bodies = true` **and** `--log-level debug` must be set; either
alone is a no-op.

Additional behaviour:

- Bodies are capped at 64 KiB; larger bodies are truncated and the log entry carries
  `request_body_truncated=true` / `response_body_truncated=true`.
- **Streaming responses are never captured.** The log records
  `response_body=<streaming: not captured>` so SSE is not buffered.
- The proxy emits a `WARN` at startup whenever this flag is on.

> **Security.** Request and response bodies routinely contain prompts, chat history,
> tool output, and sometimes credentials. With `log_bodies = true` the proxy's log
> stream **must** be treated as a secret — protect it with the same controls you
> apply to the upstream API keys. Do not enable this in production unless you have
> a concrete, time-bounded debugging need.

### On-disk state

The proxy core is deliberately near-stateless. The files it writes are:

| File                                                                                          | Purpose                                    | Mode |
| --------------------------------------------------------------------------------------------- | ------------------------------------------ | ---- |
| `<cache-dir>/llm-proxy/plugins/<ns>/<name>/<ver>/<os>_<arch>/llm-proxy-provider-<name>`      | Installed plugin binary                    | 0755 |
| `<cache-dir>/llm-proxy/plugins/<ns>/<name>/<ver>/<os>_<arch>/llm-proxy-provider-<name>.manifest.json` | Plugin manifest                   | 0644 |
| `<cache-dir>/llm-proxy/models-<backend>.json`                                                 | Model catalogue cache (TTL: 24 h)          | 0644 |
| `<config-dir>/llm-proxy/llm-proxy.lock.hcl`                                                  | Plugin version lock (written by `plugin install`, verified by `serve`) | 0644 |

> **Authorization tokens are not written by the core.** Credential storage is entirely
> the responsibility of each plugin. Plugins that persist credentials on disk are
> required to use `paths.WriteSecret` (mode 0600) and store under `<config-dir>/llm-proxy/`.
> Consult each plugin's documentation for the files it creates and how to remove
> them.

Platform paths:

| OS      | Config dir              | Cache dir                   |
| ------- | ----------------------- | --------------------------- |
| Linux   | `~/.config/llm-proxy/`  | `~/.cache/llm-proxy/`       |
| macOS   | `~/.config/llm-proxy/`  | `~/.cache/llm-proxy/`       |
| Windows | `%AppData%\llm-proxy\`  | `%LocalAppData%\llm-proxy\` |

Both directories are created with mode `0700` on first use.

### Model capability discovery and budget reporting

See [docs/endpoints.md](endpoints.md) for details on `/v1/model/info` (capability
fields, token limits, reasoning effort levels) and `/v1/usage/budget` (response
shape, special values, per-backend targeting).

[hcl]: https://github.com/hashicorp/hcl
