# /v1/models returns exact OpenAI spec; /v1/model/info is the proxy's capability extension

The proxy exposes two model-listing endpoints with deliberately different scopes:

- **`GET /v1/models`** returns exactly the fields the OpenAI spec requires —
  `id`, `object`, `created`, `owned_by` — and nothing else. No capability
  tags, no vendor metadata, no token limits. Model ids are prefix-qualified
  (`copilot/gpt-4o`).

- **`GET /v1/model/info`** is a proxy extension (no OpenAI counterpart). It
  returns rich per-model metadata derived from each backend's upstream
  catalogue: capabilities (`chat`, `tools`, `vision`, `reasoning`, `streaming`,
  `embedding`, etc.), `token_limits` (`max_input_tokens`, `max_output_tokens`,
  `max_context_tokens`), `reasoning_effort_levels`, `vendor`, `version`, and
  arbitrary `extras`.

Both endpoints share the same data source: `Backend.Models()` returns
`[]ModelInfo`, and each endpoint projects a different view of that slice. The
`ModelLister` extension is deprecated; `Backend.Models()` covers both use cases
(see ADR-0001).

Capabilities are backend-specific, not model-specific in the abstract: the same
base model served through two different backends may have different capability
tags because the backends apply different constraints (e.g. Copilot strips image
inputs, so `vision` is absent even for models that support it natively on
OpenAI).

## Consequences

- Clients that need strict OpenAI SDK compatibility use `/v1/models` and never
  see non-standard fields.
- Clients that need to discover supported features (agentic frameworks,
  capability-aware routing) use `/v1/model/info`.
- Adding a new backend capability does not break `/v1/models` consumers.

## Considered Options

- **Extend `/v1/models` with capability fields** — rejected. Breaks OpenAI
  spec compliance; any client validating response shape against the OpenAI
  schema would reject the response.
- **Require clients to probe capabilities** — rejected. Requires upstream API
  calls per model per client session; not viable at scale.
- **Static capability configuration in HCL** — rejected. Fragile; breaks when
  upstream model catalogues change. Runtime derivation from the upstream is
  preferred.
