# LLM Proxy

An authenticating proxy, exposing OpenAI-compatible API to front pluggable LLM backends.
The proxy is intended to run on the same machine as coding agents using it, stripping
any `Authorization` headers sent to it and injecting its own for each upstream provider.

Currently supported backends:

- **GitHub Copilot** (`type = "github-copilot"`)
- **IBM Bob** (`type = "ibm-bob"`)
- **OpenAI** (`type = "openai"`)

## Features

- **OpenAI API Compliance**: All backend responses are transformed to match the
  OpenAI API specification exactly, ensuring compatibility with any OpenAI client.
- HCL2 configuration:
  - single file _or_ directory of `*.hcl` files
  - supports `env("NAME")` interpolation so secrets never need to live on disk
- Prefix-based model routing (e.g. `copilot/gpt-4o`) so multiple backends can coexist
  in one proxy.
- Secure defaults:
  - binds to `127.0.0.1:4980`
  - refuses to silently default a backend
  - enforces request body size limits
  - never logs request bodies or tokens, unless explicitly enabled (troubleshooting)
- Mostly stateless. Minimal state is preserved on-disk:
  - GitHub OAuth token (required for GitHub Copilot)
  - Short-lived, Copilot API token (required for GitHub Copilot)

## Quick start

```sh
# 1. Build
task build

# 2. Authorise the proxy with your GitHub Copilot subscription (one-time).
./bin/llm-proxy login --config examples/config.hcl

# 3. Run the server.
./bin/llm-proxy serve --config examples/config.hcl

# 4. From any OpenAI-compatible client, point at http://127.0.0.1:4980 and
#    ask for a copilot-prefixed model.
curl http://127.0.0.1:4980/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"copilot/gpt-4o","messages":[{"role":"user","content":"hi"}]}'
```

See [`docs/configuration.md`](docs/configuration.md) for the full HCL schema.

## Advanced Features

### Reasoning Models and /v1/responses

The `/v1/responses` endpoint provides unified response objects with reasoning
effort support for models like OpenAI o-series and GPT-5:

```sh
curl http://127.0.0.1:4980/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "openai/o3",
    "input": "What is the meaning of life?",
    "reasoning": {"effort": "medium"}
  }'
```

The `reasoning.effort` parameter accepts `"none"`, `"minimal"`, `"low"`,
`"medium"` (default), `"high"`, `"xhigh"`, or `"max"` to control the amount
of reasoning tokens used. The response includes a `usage` object with
`input_tokens`, `output_tokens`, and `total_tokens`.

### Model Capability Discovery

The `/v1/model/info` endpoint returns extended model metadata including
capabilities, token limits, and reasoning effort levels:

```sh
curl http://127.0.0.1:4980/v1/model/info
```

This helps clients discover which models support features like:

- `chat` - Chat completion API
- `tools` - Function calling
- `vision` - Image inputs
- `reasoning` - Chain-of-thought reasoning
- `streaming` - SSE streaming
- `embeddings` - Text embeddings

Token limits (`max_input_tokens`, `max_output_tokens`, `max_context_tokens`)
and reasoning effort levels are also exposed when available from the backend.

**Note:** The standard `/v1/models` endpoint returns only OpenAI spec fields
for maximum compatibility. Use `/v1/model/info` for extended metadata.

## Listing upstream models

To see which models a configured backend's upstream catalogue currently
exposes (using the credentials the proxy already holds):

```sh
./bin/llm-proxy providers list-models <backend> -c examples/config.hcl
```

The positional argument is the HCL block label - for example `bob` when
your config contains `backend "bob" { type = "ibm-bob" ... }`. Add
`--verbose` to print supplemental metadata (vendor, capabilities, and any
provider-specific extras) under each id, or `--json` for a machine-readable
dump.

This command explicitly ignores the `models = []` config so the operator can
determine what models are available. This allows them to populate the allowlist.

For the GitHub Copilot backend, models the upstream marks as
`policy.state = "disabled"` are omitted.

## Building

`task build` compiles for the current platform; `task build:all` cross-compiles
for the following supported platforms and architectures:

- `linux` (`amd64`, `arm64`)
- `darwin` (`amd64`, `arm64`)
- `windows` (`amd64`, `arm64`)

```sh
task build         # bin/llm-proxy for host platform
task build:all     # dist/llm-proxy-<os>-<arch>[.exe] for all supported targets
```

In all cases, CGO is disabled for maximum compatibility. (`CGO_ENABLED=0`)

## Testing

```sh
task test          # unit tests with race detector and coverage
task test:cover    # tests + enforces >= 80% coverage threshold
task fuzz          # run fuzz targets (10 seconds each)
task check         # fmt + vet + lint + nilaway + govulncheck + test:cover
```

Fuzz targets live in `internal/config` (HCL loader) and `internal/router`
(model id parser).

## Project layout

```
.
├── cmd/llm-proxy/       # main package
├── internal/
│   ├── backend/         # Backend interface and registry
│   ├── cli/             # cobra subcommands: serve, login, logout, providers, version
│   ├── config/          # HCL2 schema + loader (file or directory)
│   ├── paths/           # platform-aware config/cache directory helpers
│   ├── providers/       # LLM backend implementations
│   ├── router/          # `<backend>/<model>` parsing
│   └── server/          # HTTP front door
├── docs/                # additional documentation
└── examples/config.hcl  # sample configuration
```

## License

MIT. See [`LICENSE`](LICENSE).
