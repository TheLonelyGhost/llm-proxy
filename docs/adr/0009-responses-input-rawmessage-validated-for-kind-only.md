# /v1/responses input field carries a json.RawMessage, validated only for kind

`backend.ResponsesRequest.Input` is typed as `json.RawMessage`. The server
handler validates only two properties before forwarding:

1. **Presence** — `input` must be present, non-null, and (if a string) non-empty.
   Missing, JSON-null, or empty-string inputs are rejected with HTTP 400
   `invalid_request_error`.
2. **Outer kind** — the value must be either a JSON string or a JSON array.
   If an array, every element must be a JSON object. Any other kind (number,
   boolean, plain object at the top level, or array of non-objects) is rejected
   with HTTP 400 naming the accepted shapes.

No deeper schema validation is performed. Role/content/type structure inside
array elements, and all other top-level fields beyond `model`, `stream`,
`reasoning`, and `input`, are forwarded verbatim to the backend. Full schema
validation is the upstream's responsibility, matching the proxy's posture for
other endpoints (chat, completions, embeddings).

## Consequences

- The proxy accepts both the string form (`"input": "Say hi"`) and the
  array-of-items form (`"input": [{"role":"user","content":"..."}]`) that
  modern OpenAI SDKs emit.
- `json.RawMessage` preserves byte-exact fidelity; integer ids and other
  numeric values are not mangled by Go's `float64` round-trip.
- As the OpenAI Responses API evolves, new input-item types are forwarded
  without requiring a proxy update.

## Considered Options

- **Decode `input` as `string`** — rejected (was the original bug). Caused HTTP
  400 for every modern OpenAI SDK client that sends the array form.
- **Two fields (`InputString` / `InputItems`)** — rejected. Forces every backend
  to handle both; duplicates the contract.
- **Decode to `any` / `interface{}`** — rejected. Go's JSON decoder maps
  numbers to `float64`, mangling integer ids in input items.
- **Full schema validation in the proxy** — rejected. Creates a second source of
  truth that drifts as the OpenAI spec evolves.
