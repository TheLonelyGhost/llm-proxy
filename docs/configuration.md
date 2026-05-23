# Configuration

`llm-proxy` is configured via [HCL2][hcl]. Configuration is resolved in this
order:

1. `-c` / `--config`
2. `LLM_PROXY_CONFIG` (environment variable)
3. `~/.config/llm-proxy/config.hcl`  on non-Windows, or `C:\ProgramData\llm-proxy\config.hcl` on Windows

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

  # Tracing is opt-in. When enabled, spans can be exported to stdout.
  tracing        = false
  trace_exporter = "stdout"
  trace_sampler  = "always_on"
  trace_ratio    = 1.0
  trace_service  = "llm-proxy"

  # Optional DEBUG-level body logging. Default false.
  # SECURITY: see "Body logging (DEBUG)" below before enabling.
  log_bodies = false
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

`-c` / `--config` is optional; if omitted, the proxy uses `LLM_PROXY_CONFIG` and
then the platform default path above.

Use `--log-level debug|info|warn|error` to control structured log verbosity.
Use `--trace` to enable tracing from the CLI, or configure the same settings in
the `server` block with `tracing`, `trace_exporter`, `trace_sampler`,
`trace_ratio`, and `trace_service`.

Sampling modes:

- `always_on`
- `always_off`
- `ratio`
- `parent_based_always_on`
- `parent_based_ratio`

`trace_ratio` applies only to ratio-based samplers.

## Body logging (DEBUG)

`server.log_bodies` (default `false`) toggles a DEBUG-level diagnostic log
line that includes the HTTP request body received by the proxy and the
response body sent back to the client.

Behavior:

- Both `server.log_bodies = true` **and** `--log-level debug` must be set;
  either alone is a no-op.
- Bodies are capped at 64 KiB; larger bodies are truncated and the log entry
  carries `request_body_truncated=true` / `response_body_truncated=true`.
- **Streaming responses are never captured.** When the response is streamed
  (e.g. `stream: true` on `/v1/chat/completions` or `/v1/responses`), the log
  records `response_body=<streaming: not captured>` so SSE is not buffered.
- At startup the proxy emits a `WARN`-level line whenever this flag is on, to
  make accidental enablement visible.

> **Security.** Request and response bodies routinely contain prompts, chat
> history, tool output, embedding inputs, and sometimes credentials that
> clients accidentally embed in prompts. With `log_bodies = true`, the proxy's
> log stream MUST be treated as a secret: protect it with the same controls
> you use for the upstream API keys themselves. Do not enable this flag in
> production unless you have a concrete, time-bounded debugging need.

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

Both directories are created with mode 0700 on first use. `llm-proxy logout` removes
both files.

## Endpoints

The proxy exposes:

| Method | Path                       | Description                                |
| ------ | -------------------------- | ------------------------------------------ |
| GET    | `/healthz`                 | Liveness; returns registered backend names |
| GET    | `/v1/models`               | Aggregated, prefix-qualified model list    |
| GET    | `/v1/model/info`           | Model capabilities and token limits        |
| GET    | `/v1/usage/budget`         | Per-backend spend / quota (provider ext.)  |
| POST   | `/v1/chat/completions`     | OpenAI chat completions (SSE supported)    |
| POST   | `/v1/completions`          | Legacy text completions                    |
| POST   | `/v1/embeddings`           | Embeddings                                 |
| POST   | `/v1/responses`            | Unified response objects with reasoning    |

The client's `Authorization` header is ignored entirely; the proxy injects the
backend's own credentials. Request bodies are capped at 8 MiB.

### Budget / quota reporting

`/v1/usage/budget` is a proxy extension (no OpenAI counterpart) that surfaces
the per-credential spend / ceiling for backends that expose one. See each backend's
documentation for details of each field.

- `GET /v1/usage/budget` aggregates every backend that supports it. Backends
  that do not are silently skipped. If no registered backend supports
  reporting, the proxy returns `501 Not Implemented`.
- `GET /v1/usage/budget?backend=<prefix>` returns a single item or an error:
  `404` if the prefix is unknown, `501` if the backend exists but does not
  report budget, `502` for any upstream failure.
- The response is styled like other OpenAI list endpoints (`object`/`data`,
  snake_case fields). The aggregate form looks like:

  ```json
  {
    "object": "list",
    "data": [
      {
        "object": "usage.budget",
        "backend": "ibmbob",
        "currency": "bobcoins",
        "max_budget": 100,
        "spend": 12.5,
        "remaining": 87.5,
        "unlimited": false,
        "extras": {
          "team_id": "team-1",
          "team_name": "Platform"
        }
      }
    ]
  }
  ```

  The single-backend form (with `?backend=`) returns the inner object
  directly (no `data` envelope).

  Special values:
  - `unlimited: true` means the upstream reports the account has no ceiling;
    in that case `max_budget` and `remaining` are forced to `0` because the
    numbers would be meaningless. Surface `unlimited` to humans first.
  - `max_budget: 0` with `unlimited: false` means the upstream did not report
    a ceiling (treat as unknown, not as exhausted).

### Model capability discovery

- `/v1/models` returns exact OpenAI spec format with no additional fields
- `/v1/model/info` is a proxy extension that returns per-model capabilities,
  token limits, reasoning effort levels, and vendor metadata
- Use `/v1/model/info` to discover which features each model supports before
  making requests

**Capability field values:**

| Capability   | Meaning                                                              |
| ------------ | -------------------------------------------------------------------- |
| `chat`       | Model supports `/v1/chat/completions` endpoint                       |
| `completion` | Model supports `/v1/completions` endpoint (legacy text completion)   |
| `embedding`  | Model supports `/v1/embeddings` endpoint                             |
| `tools`      | Model supports function calling / tool use                           |
| `vision`     | Model can process image inputs                                       |
| `reasoning`  | Model supports reasoning capabilities via `/v1/responses` endpoint   |
| `streaming`  | Model supports Server-Sent Events (SSE) streaming responses          |

**Token limits:**

The `token_limits` object contains:

- `max_input_tokens`: Maximum tokens allowed in input (prompt)
- `max_output_tokens`: Maximum tokens model will generate in response
- `max_context_tokens`: Total context window size (input + output)

**Reasoning effort levels:**

For models with the `reasoning` capability, `reasoning_effort_levels` lists
supported effort values for the `/v1/responses` endpoint:

- `minimal`: Minimal reasoning, fastest response (GPT-5 family)
- `low`: Faster response with less reasoning depth
- `medium`: Balanced response time and reasoning quality
- `high`: Slower response with deeper reasoning

The proxy validates `reasoning.effort` against this set and returns HTTP 400
`invalid_request_error` for any other value.

**`/v1/responses` request body:**

The `input` field accepts both shapes defined by OpenAI's Responses API:

- a plain JSON string, or
- a JSON array of input items (objects shaped like
  `{"role": "user", "content": [{"type": "input_text", "text": "..."}]}`).

The proxy validates only presence and outer kind; deeper schema validation is
left to the upstream backend. Other top-level fields (`instructions`, `tools`,
`tool_choice`, `temperature`, etc.) are forwarded verbatim.

Note: Capabilities vary by backend. For example, `copilot/gpt-4o` may have
different capabilities than `openai/gpt-4o` even though they serve the same
base model.

## Routing

Every request must include a `model` field of the form
`"<backend-prefix>/<provider-model-id>"`. Examples:

| Client sends                     | Routed to        | Forwarded model       |
| -------------------------------- | ---------------- | --------------------- |
| `copilot/gpt-4o`                 | `copilot`        | `gpt-4o`              |
| `copilot/claude-3.5`             | `copilot`        | `claude-3.5`          |
| `openrouter/openai/gpt-oss-120b` | `openrouter`     | `openai/gpt-oss-120b` |

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
