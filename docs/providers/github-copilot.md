# GitHub Copilot backend

`type = "github-copilot"`

Routes OpenAI-compatible requests to GitHub Copilot's chat, completions, and
embeddings endpoints.

> **You must have an active GitHub Copilot subscription** on the GitHub
> account you authorise with. Using this proxy with that account is subject to
> GitHub's Acceptable Use Policies. This project is not affiliated with GitHub
> or Microsoft.

## One-time login

Copilot requires a GitHub OAuth token, which the proxy obtains via the
device-code flow:

```sh
llm-proxy login --config /path/to/config.hcl
```

If `--config` is omitted, `llm-proxy` uses `LLM_PROXY_CONFIG` or the
platform default config path.

The CLI prints a URL and one-time code. Open the URL in a browser, paste the
code, and authorise the request. The resulting token is written to
`<user-config-dir>/llm-proxy/github_token.json` with mode 0600. From then on
`llm-proxy serve` runs unattended.

Tokens are removed by `llm-proxy logout --config ...`.

## Token exchange

On each API call the proxy exchanges the long-lived GitHub token for a
short-lived Copilot API token (good for roughly 30 minutes), via GitHub's
internal token endpoint. The short-lived token is cached under
`<user-cache-dir>/llm-proxy/copilot_token.json` and refreshed transparently
before expiry, or immediately if upstream returns 401.

## Configurable attributes

All fields below are optional. Defaults match the values used by the official
Copilot editor extensions at time of writing.

```hcl
backend "copilot" {
  type   = "github-copilot"
  models = ["gpt-4o", "claude-3.5-sonnet"]

  api_base              = "https://api.githubcopilot.com"
  github_api_base       = "https://api.github.com"
  github_login_base     = "https://github.com"
  oauth_client_id       = "Iv1.b507a08c87ecfe98"   # public VS Code Copilot client id
  editor_version        = "vscode/1.95.0"
  editor_plugin_version = "copilot-chat/0.22.0"
  user_agent            = "GitHubCopilotChat/0.22.0"
  integration_id        = "vscode-chat"
  request_timeout       = "60s"
}
```

### Models allow-list

`models = [...]` is the canonical list of upstream model ids this backend will
accept. The proxy uses it for:

1. **`GET /v1/models` output** — without it the proxy reports no models for
   this backend.
2. **Per-request enforcement** — requests for a model not in the list are
   rejected with 404 _before_ any upstream call, preventing accidental traffic
   to models you have not vetted.

If you leave `models` empty the proxy still routes requests, but it will not
advertise anything via `/v1/models`.

### Discovering available models

The `providers list-models` subcommand queries Copilot's catalogue (`GET
/models`) using the stored credentials and prints the ids your entitlement
currently grants:

```sh
llm-proxy providers list-models <backend-label> -c <config>
llm-proxy providers list-models <backend-label> -c <config> --verbose
llm-proxy providers list-models <backend-label> -c <config> --json
```

The positional argument is the HCL block label (for example `copilot` when
your config contains `backend "copilot" { type = "github-copilot" ... }`).
Models whose upstream `policy.state` is `disabled` (typically because your
organisation has explicitly turned them off) are filtered out; preview and
unconfigured entries are still surfaced so you can decide whether to enable
them.

With `--verbose` each id is followed by the upstream-reported vendor,
version, capabilities, family, tokenizer, and any token-limit hints.

## Headers sent upstream

For each API call the proxy sets:

| Header                   | Value                       |
| ------------------------ | --------------------------- |
| `Authorization`          | `Bearer <copilot-token>`    |
| `Editor-Version`         | `editor_version` setting    |
| `Editor-Plugin-Version`  | `editor_plugin_version`     |
| `Copilot-Integration-Id` | `integration_id`            |
| `User-Agent`             | `user_agent`                |
| `Content-Type`           | `application/json`          |

The client's `Authorization` header is _not_ forwarded.

## Budget / premium-request reporting

GitHub Copilot Pro / Pro+ / Business / Enterprise plans meter "premium
requests" against a monthly entitlement. The proxy surfaces the live
counter through the generic
[`/v1/usage/budget`](../configuration.md#budget--quota-reporting) endpoint
using the same native source the official editor extensions read from.

### Where the numbers come from

The data lives on the response of GitHub's
`GET https://api.github.com/copilot_internal/user` endpoint — the same call
the official editor extensions make to populate the "Manage Copilot" panel.
When a budget request comes in, the proxy:

1. Loads the stored GitHub OAuth token from disk.
2. Issues `GET /copilot_internal/user` with `Authorization: token <gh>`
   and the standard `Editor-Version` / `User-Agent` headers.
3. Parses `quota_snapshots`, `limited_user_quotas`, `quota_reset_date`,
   `copilot_plan`, and identifying metadata.

The endpoint uses the long-lived GitHub OAuth token directly (no separate
short-lived token exchange) and has no side effects: every call is a
single read-only GET. Crucially, it works across every plan tier —
Individual / Pro / Pro+ / **Business / Enterprise** — whereas the
`/copilot_internal/v2/token` endpoint omits `quota_snapshots` entirely for
Business/Enterprise seats and would return a misleading
`501 Not Implemented` for those users.

Relevant slice of the upstream response (Copilot Business example):

```json
{
  "login": "octocat",
  "copilot_plan": "business",
  "access_type_sku": "copilot_for_business_seat_quota",
  "chat_enabled": true,
  "quota_reset_date": "2026-06-01",
  "quota_reset_date_utc": "2026-06-01T00:00:00.000Z",
  "organization_list": [
    {"login": "contoso", "name": "Contoso, LLC"}
  ],
  "quota_snapshots": {
    "chat":        { "entitlement": 0,   "remaining": 0,    "unlimited": true,  "overage_count": 0, "overage_permitted": false },
    "completions": { "entitlement": 0,   "remaining": 0,    "unlimited": true,  "overage_count": 0, "overage_permitted": false },
    "premium_interactions": {
      "entitlement": 300,
      "remaining": 93,
      "percent_remaining": 31.0,
      "unlimited": false,
      "overage_count": 0,
      "overage_permitted": true
    }
  }
}
```

### Mapping

The proxy applies a deterministic precedence to decide which snapshot drives
the primary `max_budget` / `spend` / `remaining` fields:

1. **`quota_snapshots.premium_interactions`** — the canonical "Copilot
   budget" surface on modern paid plans. Currency: `premium_requests`.
2. **`quota_snapshots.chat`** — used when `premium_interactions` is absent
   but the upstream still returns a `chat` snapshot. Currency:
   `interactions`.
3. **`limited_user_quotas`** — legacy free-tier counter. Only remaining
   counts are reported (no entitlement), so the proxy sets `max_budget` to
   `0` and surfaces the remaining number; the key chosen is `chat` when
   present, otherwise the first key in sorted order. Currency:
   `interactions`.

Every snapshot the upstream returns (not just the chosen primary) is
exposed under `extras` with stable keys so dashboards can show the full
picture:

| Extras key                                       | Source                                        |
| ------------------------------------------------ | --------------------------------------------- |
| `primary_source`                                 | which snapshot drove the primary fields       |
| `snapshot_<name>_entitlement` / `_remaining` / `_percent_remaining` | per-snapshot numbers     |
| `snapshot_<name>_unlimited`                      | "true" when present and true                  |
| `snapshot_<name>_overage_count` / `_overage_permitted` | overage signal                          |
| `limited_user_<key>`                             | each free-tier counter, by category            |
| `limited_user_primary`                           | which limited_user counter is the primary     |
| `quota_reset_date` / `quota_reset_date_utc` / `limited_user_reset_date` | next monthly reset    |
| `chat_enabled`                                   | "true" when GitHub reports chat is entitled   |
| `copilot_plan`                                   | "individual" / "business" / "enterprise" / etc. |
| `access_type_sku`                                | upstream SKU identifier (e.g. `copilot_for_business_seat_quota`) |
| `login`                                          | GitHub login the proxy is acting as           |
| `organization_login` / `organization_name`       | first org on the seat (Business / Enterprise) |

### Special values

- `unlimited: true` (`quota_snapshots.<x>.unlimited`) → the proxy returns
  `unlimited: true` with `max_budget: 0`, `remaining: 0`, `spend: 0`.
  Render "unlimited", not the literal zeros.
- Negative `remaining` from upstream (possible when overage is permitted
  and the user has gone over) → the proxy clamps `spend` at `0` rather
  than reporting a `spend > max_budget`. The `snapshot_<x>_overage_count`
  field in `extras` preserves the raw overage signal.
- Account has no Copilot entitlement at all (no quota fields in the
  response) → the proxy returns `501 Not Implemented` with
  `ErrUnsupported`.

### Calling it

Assuming the proxy is running at `127.0.0.1:4980` and your HCL block is
labelled `copilot`:

```sh
# Aggregate every reporting backend.
curl -s http://127.0.0.1:4980/v1/usage/budget | jq

# Just GitHub Copilot.
curl -s 'http://127.0.0.1:4980/v1/usage/budget?backend=copilot' | jq
```

Sample output for a Copilot Business user:

```json
{
  "object": "usage.budget",
  "backend": "copilot",
  "currency": "premium_requests",
  "max_budget": 300,
  "spend": 207,
  "remaining": 93,
  "extras": {
    "access_type_sku": "copilot_for_business_seat_quota",
    "chat_enabled": "true",
    "copilot_plan": "business",
    "login": "octocat",
    "organization_login": "hashicorp",
    "organization_name": "HashiCorp",
    "primary_source": "premium_interactions",
    "quota_reset_date": "2026-06-01",
    "quota_reset_date_utc": "2026-06-01T00:00:00.000Z",
    "snapshot_chat_unlimited": "true",
    "snapshot_completions_unlimited": "true",
    "snapshot_premium_interactions_entitlement": "300",
    "snapshot_premium_interactions_overage_permitted": "true",
    "snapshot_premium_interactions_percent_remaining": "31",
    "snapshot_premium_interactions_remaining": "93"
  }
}
```

### Caveats

- The proxy must already be logged in (`llm-proxy login`). Without a
  stored GitHub OAuth token the budget call returns the same
  `not authenticated` error the chat calls do.
- Every `/v1/usage/budget` request triggers a fresh
  `GET /copilot_internal/user` call (no caching), per the project-wide
  policy for live quota surfaces. The call is a read-only GET against
  the stored GitHub OAuth token; it has no effect on the cached
  short-lived Copilot API token.
- `/copilot_internal/user` is an unversioned, undocumented (but widely
  used) GitHub endpoint. The official editor extensions, the community
  `copilot.lua` / `copilot.vim` clients, and various open-source proxies
  all rely on it. The proxy decodes the response defensively so unknown
  fields are ignored.

### Using it from opencode

Drop the example into your workspace:

```sh
mkdir -p .opencode/command
cp examples/opencode/command/llm-budget.md .opencode/command/
```

Then `/llm-budget` in any opencode session shows a one-line summary. See
[`examples/opencode/`](../../examples/opencode/) for the source and
customisation tips.

## Troubleshooting

- **`not authenticated: run llm-proxy login`** - run the login subcommand
  first; the proxy needs a stored GitHub token.
- **Repeated 401s from upstream** - your GitHub token may have been revoked.
  Run `llm-proxy logout` then `llm-proxy login` again.
- **`unknown model "<id>" not in backend "copilot" allow-list`** - add the
  model id to the `models` list and reload the config.

## Disclaimer

This backend talks to undocumented GitHub Copilot endpoints used by official
editor extensions. Those endpoints may change at any time; if your client id
or headers fall out of date upstream may reject requests. The defaults here
are public knowledge but not officially supported. Use at your own risk.
