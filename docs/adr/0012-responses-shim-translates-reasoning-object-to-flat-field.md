# Responses shim translates reasoning object to flat reasoning_effort for chat completions

When the Responses shim (`internal/responsesshim`) translates a `/v1/responses`
request into a `/v1/chat/completions` request, the `reasoning` object in the
Responses body must be mapped to the flat `reasoning_effort` string that the
Chat Completions endpoint expects. The two fields are structurally different:

| Endpoint | Field | Shape |
|---|---|---|
| `POST /v1/responses` | `reasoning` | Object: `{"effort": "medium", "summary": ..., "mode": ..., "context": ...}` |
| `POST /v1/chat/completions` | `reasoning_effort` | Flat string: `"medium"` |

The shim extracts only the `effort` sub-field from the `reasoning` object and
copies it as the top-level `reasoning_effort` string. All other sub-fields
(`summary`, `mode`, `context`, `generate_summary`) have no Chat Completions
equivalents and are silently dropped. The `reasoning` object itself is **not**
copied into the chat body.

```go
// Correct translation:
if v, ok := req["reasoning"]; ok {
    if obj, ok := v.(map[string]any); ok {
        if effort, ok := obj["effort"]; ok {
            chat["reasoning_effort"] = effort
        }
    }
}
```

Copying the `reasoning` object verbatim into the chat body (as a top-level
`reasoning` key) is incorrect: the Chat Completions schema does not define a
`reasoning` object, and a strictly-conforming upstream would reject it.

## Consequences

- The shim produces valid Chat Completions requests against any OpenAI-compliant
  upstream, not just ones that silently ignore unknown fields.
- `reasoning.summary`, `reasoning.mode`, and `reasoning.context` are not
  forwarded. If a future shim extension needs to carry these, explicit mappings
  must be added.

## Considered Options

- **Copy `reasoning` object verbatim as `reasoning` key in chat body** — rejected
  (was the original bug). Produces an invalid Chat Completions request; only
  worked against upstreams that happened to ignore the unknown field.
- **Drop the entire reasoning object** — rejected. Clients use `reasoning.effort`
  to control reasoning model depth; silently dropping it degrades response quality
  without any indication.
