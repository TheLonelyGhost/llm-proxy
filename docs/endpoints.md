# Endpoints reference

`llm-proxy` exposes an OpenAI-compatible HTTP API. All endpoints listen on
the address and port configured in the `server` block (default
`http://127.0.0.1:4980`).

The client's `Authorization` header is ignored on every request; the proxy
injects each backend's own credentials. Request bodies are capped at 8 MiB.

## Endpoint table

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

## Model listing — `/v1/models`

Returns the standard OpenAI list format. Each entry is prefix-qualified:
`<backend-prefix>/<upstream-model-id>` (e.g. `openai/gpt-4o`).

Only models explicitly listed in a backend's `models = [...]` allow-list are
advertised here. Backends with an empty `models` list contribute nothing to
this endpoint.

## Model capability discovery — `/v1/model/info`

A proxy extension with no OpenAI counterpart. Returns per-model capabilities,
token limits, reasoning effort levels, and vendor metadata.

Use this endpoint to discover which features each model supports before making
requests.

### Capability field values

| Capability   | Meaning                                                            |
| ------------ | ------------------------------------------------------------------ |
| `chat`       | Supports `/v1/chat/completions`                                    |
| `completion` | Supports `/v1/completions` (legacy text completion)                |
| `embedding`  | Supports `/v1/embeddings`                                          |
| `tools`      | Supports function calling / tool use                               |
| `vision`     | Can process image inputs                                           |
| `reasoning`  | Supports reasoning via `/v1/responses`                             |
| `streaming`  | Supports SSE streaming responses                                   |

> Capabilities vary by backend — `copilot/gpt-4o` may differ from
> `openai/gpt-4o` even when they serve the same base model.

### Token limits

The `token_limits` object contains:

| Field                | Description                              |
| -------------------- | ---------------------------------------- |
| `max_input_tokens`   | Maximum tokens allowed in the prompt     |
| `max_output_tokens`  | Maximum tokens the model will generate   |
| `max_context_tokens` | Total context window (input + output)    |

### Reasoning effort levels

For models with the `reasoning` capability, `reasoning_effort_levels` lists
the values supported by `reasoning.effort` in `/v1/responses` requests:

| Value     | Description                                           |
| --------- | ----------------------------------------------------- |
| `none`    | Disable reasoning (skip the internal chain-of-thought)|
| `minimal` | Minimal reasoning, fastest response                   |
| `low`     | Faster response with less reasoning depth             |
| `medium`  | Balanced response time and reasoning quality (default)|
| `high`    | Slower response with deeper reasoning                 |
| `xhigh`   | Extended reasoning for complex problems               |
| `max`     | Maximum reasoning depth, slowest response             |

The proxy validates `reasoning.effort` against this list and returns HTTP 400
`invalid_request_error` for any other value. Not all reasoning models support
every level; unsupported values are rejected by the upstream.

## Responses endpoint — `POST /v1/responses`

The `input` field accepts both shapes defined by OpenAI's Responses API:

- A plain JSON string.
- A JSON array of input items shaped like:
  ```json
  [{"role": "user", "content": [{"type": "input_text", "text": "..."}]}]
  ```

The proxy validates only presence and outer kind; deeper schema validation is
left to the upstream backend. Other top-level fields (`instructions`, `tools`,
`tool_choice`, `temperature`, etc.) are forwarded verbatim.

## Budget / quota reporting — `GET /v1/usage/budget`

A proxy extension with no OpenAI counterpart. Surfaces per-credential
spend / ceiling for backends that expose one.

- `GET /v1/usage/budget` — aggregates every backend that supports it. Backends
  that do not are silently skipped. If no registered backend supports
  reporting, the proxy returns `501 Not Implemented`.
- `GET /v1/usage/budget?backend=<prefix>` — returns a single item or an error:
  - `404` — unknown prefix
  - `501` — backend exists but does not report budget
  - `502` — upstream failure

### Response shape

The response is styled like other OpenAI list endpoints (`object`/`data`,
snake_case fields):

- Aggregate form: `{"object":"list","data":[...]}`
- Single-backend form: the inner object directly

### Special values

| Value                                   | Meaning                                                                          |
| --------------------------------------- | -------------------------------------------------------------------------------- |
| `unlimited: true`                       | Upstream reports no ceiling; `max_budget` and `remaining` are `0`. Surface `unlimited` to humans rather than the zeros. |
| `max_budget: 0` with `unlimited: false` | Upstream did not report a ceiling (treat as unknown, not exhausted).             |

See each backend's documentation for provider-specific fields returned under
`extras`.
