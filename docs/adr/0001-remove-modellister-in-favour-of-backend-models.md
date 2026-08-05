# Remove ModelLister in favour of Backend.Models()

`ModelLister` (the `ListUpstreamModels` Extension) was introduced before `Backend.Models()` returned full `ModelInfo` metadata. Now that `Backend.Models()` returns `[]ModelInfo` with capabilities, token limits, and vendor metadata, `ModelLister` has no remaining use case. It exists only as a backward-compatibility stub.

The decision is to treat `ModelLister` as fully deprecated and remove it in the next breaking cleanup. All callers — including the `providers list-models` CLI surface — should use `Backend.Models()` exclusively.

## Considered Options

- **Keep `ModelLister` as a separate Extension** — rejected. The original rationale was that `Models()` served the `/v1/models` proxy surface (operator-curated, lightweight) while `ListUpstreamModels` served human-facing tooling (full upstream catalogue). That distinction collapsed when `Backend.Models()` was changed to return `[]ModelInfo` with full metadata.
- **Remove immediately** — deferred to avoid a flag-day change across all provider implementations in a single commit. The deprecated marker signals intent; removal follows in a dedicated cleanup.
