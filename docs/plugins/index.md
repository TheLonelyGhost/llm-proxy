# Plugin System

llm-proxy supports out-of-process **sidecar plugins** — separately distributed
binaries that implement the OpenAI-compatible HTTP API on a local port. llm-proxy
manages each plugin's process lifecycle: it launches the binary, waits for it to
become healthy, monitors it, restarts it on crash (per policy), and shuts it
down cleanly on exit.

## Quick start

1. Declare the plugin in your configuration:

   ```hcl
   plugin "copilot" {
     source  = "github.com/thelonelyghost/llm-proxy-provider-copilot"
     version = "~> 1.0"
   }

   backend "gh" {
     type   = "copilot"
     models = ["gpt-4o", "claude-3.5-sonnet"]
   }
   ```

2. Install the plugin binary:

   ```sh
   llm-proxy plugin install
   ```

3. Start the proxy:

   ```sh
   llm-proxy serve
   ```

`llm-proxy serve` **hard-fails** if a declared plugin is not installed. Always
run `plugin install` after adding or updating a plugin declaration.

## Plugin block reference

```hcl
plugin "<label>" {
  # Required
  source  = "github.com/<org>/llm-proxy-provider-<name>"
  version = "~> 1.0"

  # Checksum verification (optional; auto-derived for github.com sources)
  checksum_url = "https://releases.example.com/myprovider/{version}/SHA256SUMS"

  # Health-check tuning (all optional; see defaults below)
  health_interval  = "10s"
  health_timeout   = "5s"
  health_threshold = 3

  # Restart policy (all optional)
  restart_policy          = "on-failure"  # always | on-failure | never
  restart_max_retries     = 5
  restart_initial_backoff = "1s"
  restart_backoff_cap     = "60s"
}
```

`checksum_url` supports the same placeholders as `source`: `{version}`,
`{os}`, `{arch}`. For `github.com` sources it is derived automatically and
should be omitted.

The `<label>` must be unique, lowercase, and must not collide with a compiled-in
provider type name (`openai`). A collision is a hard config-load error.

## Source addresses

### GitHub shorthand

```hcl
source = "github.com/thelonelyghost/llm-proxy-provider-ibmbob"
```

llm-proxy constructs the download URL automatically:

```
https://github.com/<org>/<repo>/releases/download/v<version>/<binary>_<version>_<os>_<arch>.tar.gz
```

The checksum file is fetched from the same release:

```
https://github.com/<org>/<repo>/releases/download/v<version>/SHA256SUMS
```

### Explicit URL with placeholders

```hcl
plugin "myprovider" {
  source       = "https://releases.example.com/myprovider/{version}/myprovider_{version}_{os}_{arch}.tar.gz"
  checksum_url = "https://releases.example.com/myprovider/{version}/SHA256SUMS"
  version      = "1.0.0"
}
```

Supported placeholders: `{version}`, `{os}`, `{arch}`.

### Local sideload

Drop files at the cache path manually and skip `plugin install`:

```
~/.cache/llm-proxy/plugins/<namespace>/<name>/<version>/<os>_<arch>/
    llm-proxy-provider-<name>
    llm-proxy-provider-<name>.manifest.json
```

## Cache layout

Plugins are cached under `paths.CacheDir()` (typically `~/.cache/llm-proxy/`
on Linux/macOS, `%LocalAppData%\llm-proxy\` on Windows):

```
~/.cache/llm-proxy/plugins/
  <namespace>/
    <name>/
      <version>/
        <os>_<arch>/
          llm-proxy-provider-<name>         # sidecar binary (mode 0755)
          llm-proxy-provider-<name>.manifest.json
```

## Lockfile

`llm-proxy plugin install` writes `llm-proxy.lock.hcl` alongside your config
file after resolving version constraints. It records the exact resolved version
and SHA-256 archive checksum per plugin per OS/arch. Commit this file to
version control for reproducible deployments.

`llm-proxy serve` verifies the cached binary checksum against the lockfile at
startup.

See [docs/lockfile.md](../lockfile.md) for the full lock file format,
resolution priority, per-platform entries, upgrade behaviour, and version
constraint syntax.

## Plugin lifecycle

For each declared plugin, `llm-proxy serve`:

1. Assigns a free ephemeral TCP port and sets `LLM_PROXY_PLUGIN_PORT=<port>`.
2. Sets `LLM_PROXY_BACKEND_LABEL`, `LLM_PROXY_BACKEND_TYPE`, and
   `LLM_PROXY_BACKEND_CONFIG` from the `backend` block (see
   [Backend context environment variables](authoring.md#backend-context-environment-variables)).
3. Launches the plugin binary.
4. Polls `GET /healthz` until HTTP 200 (readiness).
5. Routes backend traffic to `http://127.0.0.1:<port>`.
6. On SIGHUP: sends `POST /management/reload` to the plugin.
7. On crash: restarts per `restart_policy` with exponential backoff.
8. On shutdown: sends SIGTERM, waits up to `server.shutdown_timeout`, then SIGKILL.

## Tooling subcommands

Plugins may expose CLI tooling subcommands (e.g. `login`, `extract`).
Run them via:

```sh
llm-proxy plugin run <backend-label> <subcommand> [args...]
```

where `<backend-label>` is the label of a `backend` block in your configuration
(not the plugin type name). This lets you target the correct instance when two
backends use the same plugin type:

```hcl
plugin "openrouter" {
  source  = "github.com/example/llm-proxy-provider-openrouter"
  version = "~> 1.0"
}

backend "ollama" { type = "openrouter" }
backend "cloud"  { type = "openrouter" }
```

```sh
llm-proxy plugin run ollama login   # targets the "ollama" backend
llm-proxy plugin run cloud  login   # targets the "cloud" backend
```

stdin/stdout/stderr are passed through directly. The command exits with the
plugin's exit code.

Available subcommands for an installed plugin are listed in its manifest file
and surfaced in `llm-proxy plugin run <backend-label> --help`.

### Authentication

llm-proxy has no built-in `login` or `logout` commands. Authentication is
entirely the plugin's responsibility.

Plugins that require interactive credential setup (OAuth flows, browser
redirects, device codes, etc.) expose a `login` tool. Run it once before
starting the proxy, passing the backend label you want to authenticate:

```sh
llm-proxy plugin run <backend-label> login
```

Plugins that persist credentials on disk may also expose a `logout` tool to
remove them:

```sh
llm-proxy plugin run <backend-label> logout
```

`logout` is idempotent — it exits 0 even when no credentials are found.

Plugins backed by static credentials (environment variables, config fields)
do not require either step. Consult each plugin's documentation to find out
which, if any, tooling subcommands must be run before first use.

## Writing a plugin

See [docs/plugins/authoring.md](authoring.md) for the full plugin contract,
manifest format, required and optional endpoints, and how to use the
`plugintest` conformance harness.
