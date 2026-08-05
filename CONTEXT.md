# LLM Proxy

A thin OpenAI-compatible HTTP proxy that routes client requests to one or more configured LLM provider backends. Operators configure named backends via a Configuration; clients address them by embedding the Backend Name in a Qualified Model ID.

## Conventions

**Telemetry vocabulary**: Tracing uses OpenTelemetry (OTel) terminology verbatim — Trace, Span, Tracer, Exporter, Sampler, Resource, Instrumentation. These are not redefined here; use OTel's own documentation as the canonical reference. Project-specific tracing decisions (what gets instrumented, what doesn't) belong in ADRs, not this glossary.

## Language

**Credential**:
The secret material a backend holds to authenticate with its upstream. The form varies by provider (e.g. API key, OAuth access token, refresh token pair). All credentials must be stored with mode 0600 via `paths.WriteSecret` and must never be logged.
_Avoid_: token, api_key, secret (as a standalone term)

**Configuration**:
The merged, validated HCL document that defines server settings and all backend declarations. Loaded from a single `.hcl` file or a directory of `.hcl` files merged in lexicographic order. The directory-of-files form is a storage detail; the result is always a single logical Configuration.
_Avoid_: config file, HCL config, configuration document

**Registry**:
The runtime index of all active backends, keyed by Backend Name. Populated once at startup from the Configuration; read-only during request handling.
_Avoid_: backend registry, reg

**Backend**:
A configured, named, running instance of a provider. Defined by a `backend` HCL block, registered in the Registry, and addressed by clients via its Backend Name.
_Avoid_: provider (when referring to the instance), plugin

**Provider**:
The implementation type that backs one or more backends (e.g. `"github-copilot"`, `"ibm-bob"`, `"openai"`). A single provider type can be instantiated as multiple backends with different names and credentials.
_Avoid_: backend (when referring to the implementation type)

**Backend Name**:
The stable, lowercase identifier that names a backend instance. Serves as the routing prefix in a Qualified Model ID, the HCL block label, and the Registry key. Must be unique, lowercase, and free of `/` characters.
_Avoid_: prefix, label, routing prefix

**Qualified Model ID**:
The full model identifier a client sends, of the form `"<backend-name>/<canonical-model-id>"` (e.g. `"copilot/gpt-4o"`). Parsed by the router to determine which backend to route to.
_Avoid_: model id (when the backend-name prefix is present and significant)

**Canonical Model ID**:
The backend-local model name that remains after the Backend Name prefix is stripped from a Qualified Model ID (e.g. `"gpt-4o"`). This is the identifier the upstream understands.
_Avoid_: model id (when the prefix-stripped form is meant), prefix-stripped id

**Model Catalogue**:
The set of Qualified Model IDs the proxy exposes to clients for a given backend. Derived by taking the upstream's available models, filtering through the Model Allow-List (if configured), and qualifying each ID with the Backend Name. Served via `GET /v1/models`.
_Avoid_: aggregated model list, prefix-qualified model list

**Model Allow-List**:
The operator-curated set of Canonical Model IDs a backend will expose to clients. Configured via the `models` attribute of a `backend` HCL block. When absent, the upstream catalogue is used unfiltered.
_Avoid_: models list, allow-list (standalone)

**Extension**:
An optional behaviour a backend opts into by implementing a named Go interface beyond the core `Backend` interface. Discovered at runtime via type assertion. Current extensions: `BudgetReporter` (spend/quota reporting). `ModelLister` is deprecated and scheduled for removal; see ADR-0001.
_Avoid_: optional capability, optional interface, provider-extension

**Specification**:
A named operation defined by the OpenAI API specification that a backend must implement: `Chat`, `Complete`, `Embed`, and `Responses`. Corresponds directly to an OpenAI API endpoint surface (e.g. `/v1/chat/completions`, `/v1/embeddings`, `/v1/responses`). A backend that does not support a given specification returns `ErrUnsupported`, which the server translates to HTTP 501.
_Avoid_: capability (when referring to core interface methods), optional capability

**Capability**:
A hint tag indicating that a model may support a given feature (e.g. `"chat"`, `"embeddings"`, `"tool_calls"`, `"reasoning"`). Populated in `ModelInfo.Capabilities` from the upstream's model catalogue on a best-effort basis. The absence of a Capability tag does not guarantee the feature is unsupported; the presence of one indicates likely support. For example, the `"reasoning"` Capability hints that the model may honour a reasoning effort parameter if supplied. Note that the field name for that parameter differs by endpoint: `/v1/responses` uses `reasoning.effort` (inside a `reasoning` object); `/v1/chat/completions` uses the flat top-level string field `reasoning_effort`.
_Avoid_: specification (when referring to model feature hints)

**Budget**:
The per-backend record of resource consumption reported via the `BudgetReporter` extension. Composed of three distinct concepts: Spend, Max Budget, and Currency.
_Avoid_: quota, allowance

**Spend**:
The cumulative amount of a resource consumed by a backend, expressed in that backend's Currency unit.
_Avoid_: usage, consumption

**Max Budget**:
The configured ceiling on Spend for a backend. A value of zero means unknown; backends that have no ceiling report `Unlimited = true`.
_Avoid_: quota, limit, allowance

**Currency**:
The provider-defined unit name in which Spend and Max Budget are measured (e.g. `"bobcoins"` for IBM Bob, `"usd"` for USD-denominated providers).
_Avoid_: unit, denomination

**Model ID Rewrite**:
The step that replaces the Qualified Model ID in a request body's `"model"` field with the Canonical Model ID before the request is forwarded to the upstream.
_Avoid_: prefix-stripping, model field rewriting

**Response**:
The raw HTTP response a Backend returns to the server before any transformation: status code, headers, and a streaming-capable body. Produced by the `Chat`, `Complete`, `Embed`, and `Responses` Specification methods.
_Avoid_: result, output (when referring to the pre-transformation artifact)

**Transform Result**:
The normalised HTTP response produced by the Response Transform layer from a raw Response. Contains a re-encoded body, corrected status code, and client-ready headers. What the server ultimately streams to the client.
_Avoid_: response (when the transformation step has already been applied), result (standalone)

**Response Transform**:
The step that takes a raw upstream HTTP response and produces a client-ready response with normalised model IDs, error envelopes, and streaming boundaries intact. Implemented in `internal/transform/`.
_Avoid_: shim, transformation (standalone)

**Responses Shim**:
Permanent infrastructure that translates `/v1/responses` requests to `/v1/chat/completions` and back, allowing backends whose upstreams only speak Chat Completions to serve the Responses API uniformly. A specialised case distinct from the general Response Transform. Implemented in `internal/responsesshim/`.
_Avoid_: transform (when referring specifically to the responses API adapter)

**Upstream**:
The remote HTTP service a provider communicates with on behalf of a client request (e.g. the GitHub Copilot API, IBM watsonx, OpenAI API).
_Avoid_: remote, external service
