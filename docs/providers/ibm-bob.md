# IBM Bob backend

`type = "ibm-bob"`

Routes OpenAI-compatible requests to the IBM Bob backend, reproducing the
wire protocol used by the official `bob` CLI:

- API-key shape selects the upstream host and authentication scheme.
- HMAC-SHA256 request signing is applied to the modern production host.
- Instance and team ids are auto-derived from `/admin/v1/profile` on first
  use and cached for the life of the proxy process.

> This backend talks to undocumented IBM endpoints. It exists for users who
> already have a Bob entitlement and the credentials to call those endpoints.
> The project is not affiliated with IBM. Use at your own risk.

## API-key routing

| Key shape                          | Backend host                                | Auth header  | HMAC signed |
| ---------------------------------- | ------------------------------------------- | ------------ | ----------- |
| `sk-…`, `pk-…` (legacy)            | `prod.ibm-bob-staging.cloud.ibm.com`        | `Bearer …`   | no          |
| Parses as a JWT                    | `api.us-east.bob.ibm.com`                   | `Bearer …`   | yes         |
| Anything else (e.g. `bob_prod_…`)  | `api.us-east.bob.ibm.com`                   | `Apikey …`   | yes         |

The `api_base` HCL field overrides host selection unconditionally.

## Required configuration

```hcl
backend "bob" {
  type    = "ibm-bob"

  # Required. Wrap in env() so the secret never lives in the file.
  api_key = env("BOBSHELL_API_KEY")

  # Required when the resolved key targets the modern (authn) backend -
  # i.e. anything other than an sk-/pk- legacy key. Wrap in env().
  hmac_secret = env("BOB_HMAC_SECRET")
  user_agent  = "bobshell/${env("BOB_VERSION")}"

  # Routing allow-list. Clients address models as "<prefix>/<id>" where
  # <prefix> is the block label ("bob" here) and <id> is one of these.
  models = [
    "premium",
    # add your entitled models here
  ]
}
```

### Obtaining the HMAC secret

The modern Bob backend rejects unsigned requests. The secret used by the
official `bob` CLI is embedded inside the public Node bundle as
`BUILD_TIME_HMAC_SECRET`; the proxy ships a built-in extractor that recovers
it from your locally installed `bob`:

```sh
# Uses `bob` from $PATH by default.
llm-proxy ibm-bob extract

# Or point at a specific bundle / wrapper.
llm-proxy ibm-bob extract /opt/homebrew/bin/bob

# Emit shell exports you can `source` directly:
eval "$(llm-proxy ibm-bob extract --env)"

# Write the JSON to a file (mode 0600):
llm-proxy ibm-bob extract -o ~/.config/llm-proxy/bob-secrets.json
```

`llm-proxy` config follows the same precedence everywhere else: `-c` /
`--config`, then `LLM_PROXY_CONFIG`, then the platform default path.

Output (JSON form):

```json
{
  "build_time_hmac_secret": "c102b927-013e-42e4-bc65-16625f35807a",
  "version": "1.0.4"
}
```

Set the secret in your environment (e.g. via `eval $(... --env)` or a
secrets manager) and reference it from HCL via `env("BOB_HMAC_SECRET")`.

Re-run the subcommand whenever you upgrade your local `bob` install; if
the version printed by the extractor changes, the secret may have changed
too.

If you only use a legacy `sk-`/`pk-` key the proxy does not need this secret.

## Optional configuration

```hcl
backend "bob" {
  type        = "ibm-bob"
  api_key     = env("BOBSHELL_API_KEY")
  hmac_secret = env("BOB_HMAC_SECRET")

  # Override the auto-selected upstream host (rare).
  # api_base = "https://api.dev.bob.ibm.com"

  # Pin the instance/team ids and skip the /admin/v1/profile derivation.
  # When unset, the proxy fetches the profile on first use and chooses the
  # first instance and first team, mirroring `bob`'s own behaviour.
  # instance_id = "00000000-0000-0000-0000-000000000000"
  # team_id     = "00000000-0000-0000-0000-000000000000"

  # User-Agent sent to Bob. Defaults to "bobshell/1.0.4"; some upstream
  # paths gate behaviour on the major version.
  # user_agent = "bobshell/1.10.0"

  request_timeout = "300s"
  max_retries     = 6
  retry_delay     = "1s"

  models = ["premium"]
}
```

## Endpoints used upstream

| Capability        | Path on modern backend             | Path on legacy backend |
| ----------------- | ---------------------------------- | ---------------------- |
| Chat completions  | `/inference/v1/chat/completions`   | `/chat/completions`    |
| Responses (shim)  | `/inference/v1/chat/completions`   | `/chat/completions`    |
| Embeddings        | `/embeddings`                      | `/embeddings`          |
| Completions       | _not exposed_                      | `/v1/completions`      |
| Profile lookup    | `/admin/v1/profile`                | _not used_             |
| Model catalogue   | `/inference/v1/model/info`         | `/v1/model/info`       |

## Responses API shim (`/v1/responses`)

The IBM Bob upstream's native `/v1/responses` route is unreliable across
deployments. The proxy implements `/v1/responses` for `bob/<model>` as an
in-proxy translation shim over `/chat/completions`, identical in behaviour
to the shim described in the [GitHub Copilot provider](./github-copilot.md#responses-api-shim-v1responses).

Bob-specific details:

- The translated chat-completions request is dispatched through Bob's
  normal `forward` path, so Authorization headers, HMAC signing on the
  modern backend (`x-request-timestamp`, `x-request-signature`), identity
  propagation (`x-instance-id`, `x-team-id`), and retry behaviour all apply
  identically to a direct `/v1/chat/completions` call.
- Allow-list and identity bootstrap (`/admin/v1/profile`) run **before**
  the request is translated; a disallowed model or a failed profile lookup
  short-circuits with the appropriate error.
- The shared translator (under `internal/responsesshim`) is the same code
  the Copilot provider uses, so the supported subset and explicit
  non-goals — no `previous_response_id`, no built-in tools, no
  retrieval — are identical.



For each call the proxy sets:

| Header                  | Value                                                    |
| ----------------------- | -------------------------------------------------------- |
| `Authorization`         | `Bearer <key>` or `Apikey <key>` per the table above     |
| `User-Agent`            | `user_agent` setting                                     |
| `x-instance-id`         | resolved instance id (omitted for legacy keys)           |
| `x-team-id`             | resolved team id (omitted for legacy keys)               |
| `x-request-timestamp`   | RFC-3339 millisecond UTC timestamp (signed requests)     |
| `x-request-signature`   | HMAC-SHA256 hex of the canonical request (signed only)   |

The canonical signing string is `METHOD\nPATH?QUERY\nTIMESTAMP\nSHA256(body)`.

## Retry behaviour

The provider retries up to `max_retries` times with exponential backoff
(`retry_delay * 2^attempt`) on:

- HTTP 5xx
- HTTP 408, 429
- Non-5xx responses whose body contains a known transient marker
  (`Error getting user_id from apikey`, `All connection attempts failed`)

Streaming responses are not retried after the response begins.

## Identity caching

`/admin/v1/profile` is called at most once every 30 minutes per provider
instance. Restarting the proxy forces a re-derivation. To opt out entirely,
set both `instance_id` and `team_id` in HCL.

Note that the budget endpoint described below intentionally bypasses this
cache and calls `/admin/v1/profile` on every request — the operational
intent is "show the user a live balance", and 5-minute-stale figures defeat
that. Identity selection (which `instance_id`/`team_id` to use) still
respects the cache.

## Budget reporting

IBM Bob meters usage in **Bobcoins** — a per-team, per-instance allowance
exposed on the same `/admin/v1/profile` endpoint the proxy already uses to
derive identity. The proxy surfaces this via the generic
[`/v1/usage/budget`](../configuration.md#budget--quota-reporting) endpoint.

### Where the numbers come from

For an IBM Bob backend with a JWT or new-style API key, the proxy issues:

```http
GET /admin/v1/profile
Authorization: Bearer <api_key>     # or "Apikey <api_key>" for new-style keys
User-Agent: <user_agent>
x-instance-id: <resolved instance>
x-team-id:     <resolved team>
x-request-timestamp: <RFC-3339 ms UTC>
x-request-signature: <HMAC-SHA256 of canonical request>
```

The upstream response (abridged):

```json
{
  "user_id": "user-42",
  "instances": [{
    "instance_id":   "inst-1",
    "instance_name": "Acme prod",
    "teams": [{
      "id":            "team-1",
      "name":          "Platform",
      "budget_limit":  100,
      "usage":         12.5
    }]
  }]
}
```

The provider applies the same selection rules as elsewhere — `instance_id`
and `team_id` from HCL win, otherwise the first instance and first team are
used — and maps the fields to:

| `/v1/usage/budget` field | Upstream source                                  |
| ------------------------ | ------------------------------------------------ |
| `currency`               | constant `"bobcoins"`                            |
| `max_budget`             | `teams[].budget_limit` (`-1` -> `0` + `unlimited: true`) |
| `spend`                  | `teams[].usage`                                  |
| `remaining`              | `max_budget - spend`, or `0` when unlimited      |
| `extras.user_id`         | `user_id`                                        |
| `extras.instance_id`     | selected `instances[].instance_id`               |
| `extras.instance_name`   | selected `instances[].instance_name`             |
| `extras.team_id`         | selected `teams[].id`                            |
| `extras.team_name`       | selected `teams[].name`                          |

### Calling it

Assuming the proxy is running at `127.0.0.1:4980` and your HCL block is
labelled `bob` (giving the routing prefix `bob`):

```sh
# Aggregate every reporting backend.
curl -s http://127.0.0.1:4980/v1/usage/budget | jq

# Just IBM Bob.
curl -s 'http://127.0.0.1:4980/v1/usage/budget?backend=bob' | jq
```

Sample output for a single backend:

```json
{
  "object": "usage.budget",
  "backend": "bob",
  "currency": "bobcoins",
  "max_budget": 100,
  "spend": 12.5,
  "remaining": 87.5,
  "extras": {
    "instance_id": "inst-1",
    "instance_name": "Acme prod",
    "team_id": "team-1",
    "team_name": "Platform",
    "user_id": "user-42"
  }
}
```

### Caveats

- **Legacy keys are not supported.** `sk-`/`pk-` keys route to the staging
  backend, which does not expose `/admin/v1/profile`; the proxy returns
  `501 Not Implemented` in that case.
- **No caching.** Every call hits the upstream. This is by design so
  `/bobcoins`-style slash commands feel live to the user. If you wrap this
  with a slash command in opencode, run it sparingly and avoid embedding it
  in tight automation loops.
- **`unlimited: true` overrides the numbers.** When the upstream returns
  `budget_limit: -1`, surface "unlimited" rather than the literal `0`s in
  `max_budget` / `remaining`.

### Using it from opencode

Two recommended ways to bring this into an opencode session — see the
[examples directory](../../examples/opencode/) for ready-to-copy files.

1. **Slash command** (zero opencode internals):

   Create `.opencode/command/llm-budget.md` in your workspace (or
   `~/.config/opencode/command/llm-budget.md` for a user-global command):

   ```markdown
   ---
   description: Show remaining quota via the local proxy.
   ---

   !`curl -sf http://127.0.0.1:4980/v1/usage/budget \
     | jq -r '.data[] | "\(.backend): \(.spend) spent / \(.max_budget) \(.currency) -> \(.remaining) remaining"'`
   ```

   Then in opencode run `/llm-budget`. The `!`...`` ` syntax executes the
   shell command and embeds its output into the prompt; combined with the
   YAML front matter, opencode treats this as a self-contained slash
   command.

2. **Raw HTTP** — wire any other tool (status bar, shell prompt, MCP
   server, CI guard) at the same URL. The response format is stable.

## Discovering available models

The `providers list-models` subcommand queries the upstream catalogue
(`/inference/v1/model/info` for the modern backend, `/v1/model/info` for
legacy keys) so you can see which entitled models the configured key
currently exposes:

```sh
llm-proxy providers list-models <backend-label> -c <config>
llm-proxy providers list-models <backend-label> -c <config> --verbose
llm-proxy providers list-models <backend-label> -c <config> --json
```

The positional argument is the HCL block label (for example `bob` when your
config contains `backend "bob" { type = "ibm-bob" ... }`). The catalogue
follows the LiteLLM `/v1/model/info` convention: each entry is keyed by
`model_name` (falling back to `id` if upstream emits the plain OpenAI
shape) and carries per-model metadata under a nested `model_info` object,
which the CLI surfaces under `info_*` keys with `--verbose`/`--json`.

## Troubleshooting

- **`ibmbob backend "bob": hmac_secret is required for the … host`** — the
  classified API key targets the modern backend, which requires HMAC signing.
  Either provide `hmac_secret` or switch to a legacy `sk-`/`pk-` key.
- **`profile fetch failed: HTTP 401`** — the API key was rejected. Verify the
  value out-of-band with the `bob` CLI.
- **`profile fetch failed: HTTP 5xx`** — upstream is unhealthy. The proxy
  does not cache failed profile lookups, so the next request retries.
- Repeated 401s on chat requests despite a healthy profile call typically
  mean clock skew on the request signature; ensure the host clock is correct.
