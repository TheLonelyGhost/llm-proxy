# Responses Shim is permanent infrastructure

The Responses Shim (`internal/responsesshim/`) translates `/v1/responses` requests to `/v1/chat/completions` and back, allowing backends whose upstreams only speak Chat Completions to serve the Responses API. It was extracted from the Copilot provider into a shared package when IBM Bob required the same translation.

The shim is permanent infrastructure, not a temporary bridge. Not all upstreams will ever natively support `/v1/responses`, and providing a uniform Responses API surface across heterogeneous upstreams is a core proxy responsibility. Even if a specific upstream gains native Responses API support, the shim remains available for others.

## Consequences

- Providers that use the shim accept its supported subset (no `previous_response_id`, no retrieval, no built-in tools). New Responses API features must be explicitly added to the shim before shimmed backends can use them.
- When an upstream gains native Responses API support, its provider implementation should bypass the shim and speak the API directly. The shim is not retired — it continues serving other backends.
- The shim's supported subset must be documented per-provider so operators know which Responses API features are available on each backend.

## Considered Options

- **Treat as temporary, retire when upstreams go native** — rejected. The proxy supports multiple heterogeneous upstreams; some will never gain native Responses API support. A permanent shim is the only way to provide uniform API surface.
