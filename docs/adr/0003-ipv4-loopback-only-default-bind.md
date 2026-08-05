# Default bind address is IPv4 loopback only

The proxy defaults to binding on `127.0.0.1` (IPv4 loopback). IPv6 is not currently supported. This is a deliberate constraint, not an oversight — the trust model (ADR-0002) relies on the loopback interface as the network-level security boundary, and supporting dual-stack or IPv6-only environments would require revisiting that boundary.

If IPv6 loopback support is added in the future, the equivalent address (`::1`) should be used as the default for IPv6-only environments, and the bind logic should be updated consistently across `config.DefaultAddress`, documentation, and any IPv6-aware trust model reasoning.
