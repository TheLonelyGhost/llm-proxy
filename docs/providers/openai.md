# OpenAI-compatible backend

`type = "openai"`

Routes OpenAI-compatible requests to the official OpenAI API or any third-party
service that implements the same interface (e.g., Azure OpenAI, OpenRouter,
local inference servers).

## Authentication

For the official OpenAI API (default `base_url`), an API key is required:

```hcl
backend "openai" {
  type    = "openai"
  api_key = env("OPENAI_API_KEY")
  models  = ["gpt-4", "gpt-3.5-turbo"]
}
```

For custom endpoints that don't require authentication, you can omit the API
key:

```hcl
backend "local-llm" {
  type     = "openai"
  base_url = "http://localhost:8080/v1"
  models   = ["llama-3.1-70b"]
}
```

## Configurable attributes

| Attribute         | Type     | Default                        | Description                                   |
| ----------------- | -------- | ------------------------------ | --------------------------------------------- |
| `api_key`         | `string` | _(required unless base_url)_   | Bearer token sent in Authorization header     |
| `base_url`        | `string` | `https://api.openai.com/v1`    | Base URL for the OpenAI-compatible API        |
| `models`          | `list`   | `[]`                           | Allow-list of model ids this backend accepts  |
| `request_timeout` | `string` | `"60s"`                        | HTTP timeout (e.g. `"30s"`, `"2m"`)           |

### Example configuration

```hcl
backend "openai" {
  type    = "openai"
  api_key = env("OPENAI_API_KEY")
  models  = ["gpt-4", "gpt-4-turbo", "gpt-3.5-turbo"]
  
  # Optional: adjust timeout for longer-running requests
  request_timeout = "120s"
}
```

### Using with alternative providers

The OpenAI provider works with any service that implements the OpenAI API:

```hcl
# Azure OpenAI
backend "azure" {
  type     = "openai"
  base_url = "https://<your-resource>.openai.azure.com/openai/deployments/<deployment>/v1"
  api_key  = env("AZURE_OPENAI_API_KEY")
  models   = ["gpt-4"]
}

# OpenRouter
backend "openrouter" {
  type     = "openai"
  base_url = "https://openrouter.ai/api/v1"
  api_key  = env("OPENROUTER_API_KEY")
  models   = ["anthropic/claude-3-opus", "meta-llama/llama-3-70b"]
}

# Local inference server (e.g., Ollama, vLLM, text-generation-webui)
backend "local" {
  type     = "openai"
  base_url = "http://localhost:8000/v1"
  models   = ["mistral-7b-instruct"]
  # No api_key needed for local servers
}
```

## Models allow-list

`models = [...]` is the canonical list of upstream model ids this backend will
accept. The proxy uses it for:

1. **`GET /v1/models` output** — without it the proxy reports no models for
   this backend.
2. **Per-request enforcement** — requests for a model not in the list are
   rejected with 404 _before_ any upstream call, preventing accidental traffic
   to models you have not vetted.

If you leave `models` empty the proxy still routes requests, but it will not
advertise anything via `/v1/models`.

## Supported endpoints

The OpenAI provider forwards requests to the following endpoints:

- **Chat completions**: `POST /chat/completions` (streaming and non-streaming)
- **Legacy completions**: `POST /completions` (streaming and non-streaming)
- **Embeddings**: `POST /embeddings`
- **Responses**: `POST /v1/responses` — forwarded verbatim to the OpenAI
  Responses API. Supports `reasoning.effort` values `"none"`, `"minimal"`,
  `"low"`, `"medium"` (default), `"high"`, `"xhigh"`, and `"max"` as defined
  by the OpenAI `ReasoningEffort` schema. No in-proxy translation is applied;
  the upstream's native Responses API handles the request.

## Headers sent upstream

For each API call the proxy sets:

| Header            | Value                             |
| ----------------- | --------------------------------- |
| `Authorization`   | `Bearer <api_key>` (if provided)  |
| `Content-Type`    | `application/json`                |
| `Accept`          | `application/json`                |

The client's `Authorization` header is _not_ forwarded.

## Security notes

- API keys are read from HCL configuration at startup. Use `env("VAR_NAME")`
  rather than hardcoding secrets.
- The proxy binds to `127.0.0.1` by default. Never expose it directly to
  untrusted networks without additional authentication.
- When using custom `base_url` endpoints, ensure they are trusted sources.

## Troubleshooting

- **`api_key is required unless base_url is specified`** - When using the
  default OpenAI endpoint, you must provide an API key. Either set `api_key` or
  use a custom `base_url`.
- **`unknown model "<id>" not in backend "<name>" allow-list`** - Add the
  model id to the `models` list and reload the config.
- **Connection refused** - Check that the `base_url` is correct and the
  upstream service is running.
- **401 Unauthorized** - Verify your API key is valid for the configured
  endpoint.
