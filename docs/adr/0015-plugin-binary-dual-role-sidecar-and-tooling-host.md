# Plugin binary has a dual role: HTTP sidecar and CLI tooling host

A plugin is a single binary that llm-proxy manages in two distinct modes. During `serve`, the proxy launches the binary as a long-running HTTP sidecar, assigns it an ephemeral loopback port via `LLM_PROXY_PLUGIN_PORT`, and routes matching backend requests to it. For operator tooling (authentication setup, model discovery, credential teardown), the proxy exec-s the same binary with `--tool <subcommand>` and passes stdin/stdout/stderr through directly, then exits.

The `--tool` interface is the canonical way to expose backend-specific CLI operations. llm-proxy invokes it as:

```sh
your-binary --tool <subcommand> [args...]
```

The set of available tools is declared in the plugin manifest under `tools`. Before exec-ing, llm-proxy sets `LLM_PROXY_BACKEND_LABEL`, `LLM_PROXY_BACKEND_TYPE`, and `LLM_PROXY_BACKEND_CONFIG` so the tool knows which backend instance it is running for.

## Considered Options

- **Separate binaries for sidecar and tooling** — rejected. Shipping two binaries per plugin doubles the release surface and splits the code that shares credentials, configuration parsing, and upstream clients. One binary, two dispatch modes, keeps everything co-located.
- **Subcommand rather than `--tool` flag** — rejected. `--tool` reads as a mode switch rather than an argument, which makes it harder to accidentally invoke tooling mode via a misconfigured `PATH` entry or shell alias.
- **Separate dedicated CLI separate from the plugin binary** — rejected for the same reason as two binaries above, and additionally because it would require the proxy to know about and ship per-plugin CLIs, defeating the purpose of the plugin model.

## Consequences

Plugin authors must implement `--tool` dispatch in their binary. The `plugintest` conformance harness does not currently exercise tool subcommands; authors are responsible for testing them independently.
