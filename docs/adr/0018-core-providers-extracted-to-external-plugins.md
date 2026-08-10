# Core providers extracted to external plugin binaries

The GitHub Copilot and IBM Bob provider implementations, which shipped as compiled-in backends in v0.1.0, were removed from the core binary and republished as external plugins:

- `github.com/thelonelyghost/llm-proxy-provider-copilot`
- `github.com/thelonelyghost/llm-proxy-provider-ibmbob`

Operators who used these backends must add a `plugin` block pointing to the relevant module and update their `backend` blocks accordingly.

The motivation is the same principle that drives the plugin system generally: provider-specific behaviour (OAuth flows, browser SSO, vendor-specific APIs) has a faster release cadence and higher blast radius than the proxy core. Keeping them in core meant every provider bug or credential-format change required a core release. As external plugins they can be versioned and updated independently.

## Considered Options

- **Keep built-in providers as compiled-in fallbacks and also support plugins** — rejected. Two code paths for the same backends create permanent maintenance surface. Plugin authors would reasonably ask why their provider doesn't get the same treatment as "blessed" built-ins.
- **Keep providers in core indefinitely** — rejected. The IBM Bob SSO flow and Copilot OAuth device code both involve browser interaction, secrets storage, and vendor-specific retry logic. Their change rate makes them a poor fit for core, where stability is the priority.

## Consequences

Operators upgrading from v0.1.0 must explicitly install and reference the new plugin packages. `llm-proxy plugin install` handles binary download; the `llm-proxy.lock.hcl` lock file pins the resolved version.
