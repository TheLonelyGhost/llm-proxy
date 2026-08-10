# Authentication is entirely a plugin concern; no built-in login or logout commands

The proxy has no built-in `login` or `logout` commands. All credential acquisition and teardown is the plugin's responsibility. Plugins that require interactive setup must expose a `login` tool in their manifest; operators run it once before starting the proxy:

```sh
llm-proxy plugin run <backend-label> login
```

Similarly, plugins that persist credentials on disk should expose a `logout` tool:

```sh
llm-proxy plugin run <backend-label> logout
```

The convention names are `login` and `logout`. Credentials are written under `paths.ConfigDir()` with mode `0600`.

This replaces the former top-level `llm-proxy login` and `llm-proxy logout` commands, which were hard-coded to the GitHub Copilot OAuth flow and could not generalise to arbitrary providers.

## Considered Options

- **Keep top-level `llm-proxy login` and route it to the active backend** — rejected. There can be multiple backends of the same type; a top-level command cannot unambiguously select one. The `<backend-label>` argument in `llm-proxy plugin run` eliminates ambiguity.
- **Embed credential acquisition inside `serve`** — rejected. Interactive flows (browser redirects, device codes, prompted API keys) block the server start and cannot be deferred cleanly. The operator ceremony belongs before `serve`.
- **Store credentials in the proxy config file** — rejected for static credentials this is already supported via `env()` interpolation; for interactive flows it is impractical and forces secrets into a file the operator must maintain manually.
