# Authoring a Plugin

A plugin is a single binary that serves as both an OpenAI-compatible HTTP
sidecar and a CLI tooling host. llm-proxy manages its process lifecycle.

## Required endpoints

Your binary must implement:

| Endpoint | Method | Purpose |
|---|---|---|
| `/healthz` | GET | Readiness and liveness. Must return HTTP 200. |
| `/management/reload` | POST | SIGHUP propagation. May be a no-op; must return 2xx. |
| `/v1/models` | GET | Model listing. Returns OpenAI-format `{"object":"list","data":[...]}`. |
| `/v1/chat/completions` | POST | Chat completions (streaming and non-streaming). |

## Optional endpoints

Return **501 Not Implemented** (not 404) for endpoints you do not support:

| Endpoint | Method |
|---|---|
| `/v1/completions` | POST |
| `/v1/embeddings` | POST |
| `/v1/responses` | POST |

> **Why 501 and not 404?** llm-proxy translates 501 into a clean "capability
> not supported" error for clients. A 404 is ambiguous with a routing error
> and produces a confusing error message.

## Response duration

llm-proxy does not set a total time limit on requests to your sidecar. A
request continues until your sidecar completes the response or the client
disconnects. Long streams and slow non-streaming responses are not cut off.
When the client disconnects, llm-proxy cancels the request to your sidecar.

## Port binding

llm-proxy assigns a free ephemeral port and passes it via the environment
variable named in your manifest's `port_env_var` field (default:
`LLM_PROXY_PLUGIN_PORT`). Your binary must bind to `127.0.0.1:<port>`.

```go
port := os.Getenv("LLM_PROXY_PLUGIN_PORT")
ln, _ := net.Listen("tcp", "127.0.0.1:" + port)
```

## Backend context environment variables

llm-proxy sets three environment variables when it starts your binary. It sets
them in sidecar mode (`llm-proxy serve`) and in tooling mode
(`llm-proxy plugin run`). Use them to find the backend instance that your
binary operates for.

| Variable | Value |
|---|---|
| `LLM_PROXY_BACKEND_LABEL` | The `backend` block label (e.g. `ollama`) |
| `LLM_PROXY_BACKEND_TYPE` | The plugin type name (e.g. `copilot`) |
| `LLM_PROXY_BACKEND_CONFIG` | JSON object with all backend attributes |

`LLM_PROXY_BACKEND_CONFIG` is a JSON object. Its keys are `name`, `type`,
`models` (if the block sets it), and each provider-specific attribute in the
`backend` block. llm-proxy evaluates `env()` references before it encodes the
object. It encodes each attribute as follows:

- A string, number, or bool becomes a JSON string, number, or bool.
- A list or tuple becomes a JSON array.
- A map or object becomes a JSON object.
- `null` becomes JSON `null`.

For example, this block:

```hcl
backend "ollama" {
  type     = "openrouter"
  base_url = "http://localhost:11434"
  models   = ["llama3"]
  headers  = { "X-Team" = "llm" }
}
```

gives this value:

```json
{"base_url":"http://localhost:11434","headers":{"X-Team":"llm"},"models":["llama3"],"name":"ollama","type":"openrouter"}
```

Use these variables to select the correct credential file, endpoint URL, or
other instance-specific configuration. Two backends can use the same plugin
type (e.g. `ollama` and `cloud` both use `type = "openrouter"`). In sidecar
mode, llm-proxy starts one process for each `backend` block.
`LLM_PROXY_BACKEND_LABEL` tells each process which backend it operates for.

## Tooling mode

Your binary may expose CLI tooling subcommands. llm-proxy invokes them as:

```sh
your-binary --tool <subcommand> [args...]
```

stdin/stdout/stderr are passed through directly. Implement `--tool` as a
top-level flag or subcommand that dispatches to the named function and exits.

### The `login` convention

llm-proxy has no built-in `login` command. Authentication is entirely the
plugin's concern.

**If your plugin requires interactive credential setup** (OAuth flows, device
codes, browser redirects, API-key prompts, etc.) you **must** expose a tool
named `login` in your manifest. Operators run it once before starting the
proxy, specifying the `backend` block label they want to authenticate:

```sh
llm-proxy plugin run <backend-label> login
```

Behaviour requirements:

- Interactively acquire credentials using whatever flow the provider requires
  (device code, browser redirect, prompted API key, etc.).
- Persist the credentials to `paths.ConfigDir()` (typically
  `~/.config/llm-proxy/` on Linux/macOS). **Never** write to a world-readable
  path; use mode `0600` for credential files.
- Exit **0** on success, non-zero on failure.
- On failure, print a clear, human-readable message to stderr.
- Document the tool and any prerequisites in your plugin's README under an
  **"Authentication"** heading.

Plugins backed by static, long-lived credentials (e.g. an API key in the
config file or an environment variable) do not need a `login` tool at all.

### The `list-models` convention

Plugins that can enumerate available models from their upstream provider
**should** expose a tool named `list-models`.

```sh
llm-proxy plugin run <backend-label> list-models
```

This tool is distinct from the `/v1/models` HTTP endpoint served while the
proxy is running. It is a standalone CLI path intended for operators who want
to discover available models without starting the proxy — for example, to
learn which model IDs to place in an allow-list or to verify that credentials
are working.

Behaviour requirements:

- Query the upstream provider's model catalogue using whatever credentials the
  plugin has stored (written by `login`, or read from the config file / env).
- Print each model on stdout. Plain text (one model ID per line) is
  sufficient; structured output (e.g. JSON) is permitted if the plugin
  provides a flag for it.
- The output **must not** be filtered or constrained by the operator's
  configured `models` allow-list. This tool is for discovery; the allow-list
  is an operator-level concern applied by the proxy at serve time, not here.
- Exit **0** on success, non-zero on failure.
- On failure, print a clear, human-readable message to stderr.
- Document the tool in your plugin's README under a **"Models"** or
  **"Available models"** heading.

Plugins backed by a static, well-known model list do not need this tool.

### The `logout` convention

If your plugin persists credentials on disk, consider also exposing a tool
named `logout`. It is the counterpart to `login`:

```sh
llm-proxy plugin run <backend-label> logout
```

Behaviour requirements:

- Remove (or invalidate) all credentials written by `login`, including any
  cached tokens, refresh tokens, or session files under `paths.ConfigDir()`.
- If no credentials are found, exit **0** silently (idempotent).
- Exit **0** on success, non-zero on failure.
- On failure, print a clear, human-readable message to stderr.
- Document the tool in your plugin's README alongside `login`.

Plugins that have no persisted credentials do not need a `logout` tool.

## Manifest file

Ship `llm-proxy-provider-<name>.manifest.json` alongside your binary in the
release archive:

```json
{
  "name": "myprovider",
  "version": "1.2.0",
  "protocol_version": 1,
  "port_env_var": "LLM_PROXY_PLUGIN_PORT",
  "endpoints": {
    "health": "/healthz",
    "reload": "/management/reload"
  },
  "tools": [
    { "name": "login",       "description": "Authenticate via browser SSO" },
    { "name": "logout",      "description": "Remove persisted credentials" },
    { "name": "list-models", "description": "List upstream models available to the configured credential" },
    { "name": "budget",      "description": "Show current spend and budget limit for myprovider" },
    { "name": "extract",     "description": "Extract secret from installed CLI" }
  ]
}
```

### Fields

| Field | Required | Default | Description |
|---|---|---|---|
| `name` | ✅ | — | Plugin name (matches plugin block label) |
| `version` | ✅ | — | Plugin version string |
| `protocol_version` | ✅ | — | Must be `1` for current llm-proxy |
| `port_env_var` | | `LLM_PROXY_PLUGIN_PORT` | Env var carrying the assigned port |
| `endpoints.health` | | `/healthz` | Health/readiness endpoint path |
| `endpoints.reload` | | `/management/reload` | Reload endpoint path |
| `tools` | | `[]` | List of tooling subcommands |

llm-proxy hard-fails if `protocol_version` exceeds its supported maximum.

## Release archive layout

Each GitHub release must include per-platform archives named:

```
llm-proxy-provider-<name>_<version>_<os>_<arch>.tar.gz
```

Each archive must contain:

```
llm-proxy-provider-<name>           # binary (mode 0755)
llm-proxy-provider-<name>.manifest.json
```

And a `SHA256SUMS` file at the release root:

```
<sha256>  llm-proxy-provider-<name>_<version>_linux_amd64.tar.gz
<sha256>  llm-proxy-provider-<name>_<version>_linux_arm64.tar.gz
...
```

## Conformance test harness

Verify your plugin satisfies the contract using the `plugintest` package:

```go
package myprovider_test

import (
    "testing"
    "github.com/thelonelyghost/llm-proxy/plugintest"
)

func TestConformance(t *testing.T) {
    plugintest.Run(t, plugintest.Options{
        Binary: "./bin/llm-proxy-provider-myprovider",
    })
}
```

The harness:
- Launches your binary with a randomly assigned port
- Exercises all required endpoints
- Verifies optional endpoints return 501 (not 404) when unsupported
- Checks graceful shutdown on SIGTERM
- Reports each check as a named sub-test

## Graceful shutdown

Your binary must handle SIGTERM and exit cleanly, draining in-flight requests
within a reasonable grace period. llm-proxy sends SIGTERM first, then SIGKILL
after `server.shutdown_timeout` (default 30s).
