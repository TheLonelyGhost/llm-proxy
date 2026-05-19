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
