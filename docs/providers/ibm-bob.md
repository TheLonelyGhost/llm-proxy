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
| Embeddings        | `/embeddings`                      | `/embeddings`          |
| Completions       | _not exposed_                      | `/v1/completions`      |
| Profile lookup    | `/admin/v1/profile`                | _not used_             |
| Model catalogue   | `/inference/v1/model/info`         | `/v1/model/info`       |

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
