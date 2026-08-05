# Model Catalogue Cache: on-disk + in-memory, per-backend, 24-hour TTL

The proxy caches each backend's unfiltered upstream model catalogue to avoid
redundant upstream calls on every `/v1/models` and `/v1/model/info` request.

## Decisions

### What is cached

The **unfiltered** upstream model list (`[]ModelInfo`) — the raw result of the
upstream fetch, before the Model Allow-List is applied. This means adding a
model to the allow-list takes effect immediately without requiring a cache
invalidation.

### Cache granularity

One file per backend, named by Backend Name (e.g. `models-copilot.json`),
stored under `paths.CacheDir()`. Independent invalidation and inspection per
backend; no shared-file write contention.

### Cache format

JSON. The file is a single object with two fields:

```json
{
  "fetched_at": "<RFC3339 timestamp>",
  "models": [...]
}
```

`fetched_at` is an embedded timestamp used to compute age. mtime is
intentionally not used — it is fragile (reset by backup tools, `cp`, editors)
and not portable across filesystems.

### TTL and invalidation

24 hours from `fetched_at`. On every call the decorator reads the in-memory
copy first; if absent or stale it reads the disk file; if the disk file is
absent or stale it calls the upstream fetch function.

SIGHUP (via the `Reloadable` extension) clears the in-memory copy. The next
request after SIGHUP triggers a fresh upstream fetch regardless of disk file
age, and overwrites the disk file. Operators can also delete the disk file
manually; the cache treats a missing file identically to a stale one.

### Stale-data fallback

When the cache is stale **and** the upstream call fails, the expired cached
data is served with a WARN-level log. This preserves the "degraded-but-usable"
posture established elsewhere in the codebase (see `AllowList.Models` stub
fallback). If no cache exists at all and the upstream fails, the existing stub
fallback path in `AllowList.Models` continues to apply.

### Storage permissions

Cache files are written with mode 0644 (not 0600). Model catalogue data is not
sensitive; `paths.WriteSecret` is not used. Writes are atomic (temp file +
rename) to prevent partial reads.

### Architecture

A new `internal/modelcache` package exposes a **decorator** function:

```go
func Wrap(name string, ttl time.Duration, fetch func(context.Context) ([]backend.ModelInfo, error)) func(context.Context) ([]backend.ModelInfo, error)
```

The returned function owns the full read-check-fetch-write cycle, including:
- in-memory copy (mutex-guarded `[]ModelInfo` + `fetchedAt`)
- disk load on cold start
- upstream fetch on miss or expiry
- atomic disk write after a successful upstream fetch
- stale-data fallback with WARN log on upstream failure

Providers register their upstream fetch once at construction time and call the
returned function from their `Models()` implementation. No per-provider
TTL logic; no duplication.

Each provider that opts into caching also implements `Reloadable.Reload()` to
clear its in-memory copy.

## Consequences

- Repeated `/v1/models` and `/v1/model/info` requests within a 24-hour window
  make zero upstream calls and zero disk reads (served from memory).
- A proxy restart re-warms from disk on the first request; no upstream call is
  needed if the disk cache is fresh.
- Allow-list changes take effect immediately without cache invalidation.
- Operators can force a fresh upstream fetch via `kill -HUP <pid>` or by
  deleting the cache file.
- The `internal/modelcache` package has no knowledge of providers; any future
  backend gets caching for free by calling `Wrap`.

## Considered Options

- **In-memory only** — rejected. Cache does not survive restarts; a daily
  restart would cause one cold upstream call per backend per day.
- **mtime-based invalidation** — rejected. Fragile; backup tools, `cp`, and
  editors silently reset the clock.
- **Server-level decorator around `Backend.Models()`** — rejected. By that
  point filtering has already been applied; the cache would store the
  post-filter result, breaking the requirement to cache the unfiltered catalogue.
- **Passive load/store library** — rejected. Duplicates the TTL-check-and-refresh
  pattern across every provider. Decorator pattern centralises it.
- **No SIGHUP invalidation** — rejected. Operators would have to wait up to 24
  hours or manually delete cache files after a known upstream catalogue change.
