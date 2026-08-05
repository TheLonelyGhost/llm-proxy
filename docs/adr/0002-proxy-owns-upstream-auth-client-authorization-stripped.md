# Proxy owns all upstream authentication; client Authorization is stripped

The proxy is designed to run on a trusted interface (default `127.0.0.1`) and act as the sole owner of upstream authentication. It deliberately strips the client's `Authorization` header before forwarding any request to an upstream, and uses only the backend's own Credentials for outbound authentication.

This means all clients that can reach the proxy are implicitly trusted — there is no per-client authentication or authorisation layer. The security boundary is the network interface the proxy binds to, not a credential check at the HTTP layer.

## Consequences

- Adding per-client authentication would be a meaningful design change requiring a new security boundary (e.g. a bearer token per client, mTLS, or a reverse proxy in front).
- Operators are responsible for ensuring only trusted processes can reach the proxy's bound address.
- Any future feature that passes client-supplied credentials through to the upstream (rather than the backend's own Credentials) would contradict this decision and must be explicitly justified.

## Considered Options

- **Forward client Authorization to upstream** — rejected. This would require the proxy to know which upstream format each client credential belongs to, leak client secrets through the proxy's log stream, and undermine the abstraction the proxy provides.
- **Add per-client authentication at the proxy** — deferred. The current use case (local developer tooling, single-operator deployment) does not require it. The loopback default is the practical enforcement mechanism.
