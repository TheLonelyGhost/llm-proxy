// Default proxy configuration. Copy to ~/.config/llm-proxy/ or pass via
// `llm-proxy serve --config examples/config.hcl`.

server {
  address = "127.0.0.1"
  port    = 4980
}

# Each `backend "<prefix>" { ... }` block defines an upstream provider.
# Clients address it by sending model = "<prefix>/<provider-model-id>".
backend "copilot" {
  type = "github-copilot"

  # Allow-list of upstream model ids served by this backend. Clients ask
  # for "copilot/gpt-4o" and the proxy forwards it to Copilot as "gpt-4o".
  models = [
    "gpt-4o",
    "gpt-4o-mini",
    "claude-3.5-sonnet",
    "o1-mini",
  ]

  # Optional: override defaults. Most users do not need these.
  # api_base              = "https://api.githubcopilot.com"
  # request_timeout       = "60s"
  # oauth_client_id       = "Iv1.b507a08c87ecfe98"
  # editor_version        = "vscode/1.95.0"
  # editor_plugin_version = "copilot-chat/0.22.0"
  # user_agent            = "GitHubCopilotChat/0.22.0"
  # integration_id        = "vscode-chat"
}

# IBM Bob (bobshell) backend. See docs/providers/ibm-bob.md for the key-shape
# routing table and how to obtain the build-time HMAC secret.
backend "bob" {
  type = "ibm-bob"

  api_key     = env("BOBSHELL_API_KEY")
  hmac_secret = env("BOB_HMAC_SECRET") # not required for sk-/pk- legacy keys

  models = [
    "premium",
  ]

  # Optional. Override only when bob's CLI flags would tell you to.
  # api_base    = "https://api.dev.bob.ibm.com"
  # instance_id = "00000000-0000-0000-0000-000000000000"
  # team_id     = "00000000-0000-0000-0000-000000000000"
  # user_agent  = "bobshell/0.7.0"

  request_timeout = "300s"
  max_retries     = 6
  retry_delay     = "1s"
}
