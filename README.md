# LLM Proxy

An authenticating proxy, exposing OpenAI-compatible API to front pluggable LLM backends.
The proxy is intended to run on the same machine as coding agents using it, stripping
any `Authorization` headers sent to it and injecting its own for each upstream provider.

## Features

- **OpenAI API compliance** — all backend responses are normalised to the OpenAI API
  specification, so any OpenAI-compatible client works without modification.
- **Prefix-based model routing** — `<backend>/<model>` (e.g. `copilot/gpt-4o`) lets
  multiple backends coexist in one proxy instance.
- **HCL2 configuration** — single file or directory of `*.hcl` files; `env("NAME")`
  interpolation keeps secrets out of source control.
- **Plugin system** — out-of-process provider plugins are downloaded, verified, and
  lifecycle-managed by the proxy.
- **Secure defaults** — binds to `127.0.0.1:4980`; strips client `Authorization`
  headers; enforces request body size limits; no backend silently defaulted.

## Quick start

```sh
# 1. Build
task build

# 2. Authorise the proxy with your provider (one-time, if the plugin requires it).
./bin/llm-proxy login --config examples/config.hcl

# 3. Run the server.
./bin/llm-proxy serve --config examples/config.hcl

# 4. From any OpenAI-compatible client, point at http://127.0.0.1:4980 and
#    ask for a backend-prefixed model.
curl http://127.0.0.1:4980/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"copilot/gpt-4o","messages":[{"role":"user","content":"hi"}]}'
```

## Backends

| Backend | Type | Source |
|---|---|---|
| OpenAI-compatible | `openai` (built-in) | [docs/providers/openai.md](docs/providers/openai.md) |
| GitHub Copilot | plugin | [llm-proxy-provider-copilot](https://github.com/thelonelyghost/llm-proxy-provider-copilot) |
| IBM Bob | plugin | [llm-proxy-provider-ibmbob](https://github.com/thelonelyghost/llm-proxy-provider-ibmbob) |

Plugin backends are declared with a `plugin` block and installed with
`llm-proxy plugin install` before starting the server. See
[docs/plugins/index.md](docs/plugins/index.md) for the full plugin reference,
and each plugin's own repository for provider-specific configuration,
authentication, and tooling.

Some plugins expose additional CLI tooling subcommands (e.g. login flows,
secret extraction). Consult the plugin's documentation for details on which
subcommands are available and whether any are required before first use.

## Configuration

Configuration is HCL2 and supports a single file or a directory of `*.hcl`
files. See [`docs/configuration.md`](docs/configuration.md) for the full
schema, endpoint reference, routing rules, and security notes.

## CLI reference

| Command | Description |
|---|---|
| `llm-proxy serve` | Start the proxy server |
| `llm-proxy providers list-models <backend>` | Query a backend's upstream model catalogue |
| `llm-proxy plugin install` | Download and verify declared plugins |
| `llm-proxy plugin run <label> <subcommand>` | Run a plugin tooling subcommand (e.g. `login`) |

Pass `-c` / `--config` to any command to specify a config file or directory.
Run any command with `--help` for the full flag list.

## Development

```sh
task build         # compile for current platform
task build:all     # cross-compile for all supported targets
task check         # fmt + vet + lint + nilaway + govulncheck + test:cover
task test:cover    # unit tests + 80% coverage threshold
```

See [`AGENTS.md`](AGENTS.md) for the full contributor workflow, architecture
overview, and instructions for adding a new backend.

## License

MIT. See [`LICENSE`](LICENSE).
