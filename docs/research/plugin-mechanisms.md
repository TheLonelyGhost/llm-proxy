# Research: Go Plugin Mechanisms for CGO-Disabled, Cross-Platform Binaries

## Summary

Go's native `plugin` package is disqualified by the hard `CGO_ENABLED=0` constraint. Of the four
evaluated alternatives, **compile-time module composition** (option 3) is the lowest-risk,
highest-fidelity approach for `llm-proxy` today: providers become ordinary Go packages compiled into
the main binary, matching the project's existing `internal/providers/<name>/` pattern. The
**out-of-process HTTP sidecar** (option 4) is the right future escape hatch for independent binary
distribution. `hashicorp/go-plugin` adds serious complexity for a streaming HTTP workload; WASM
remains immature for networking-heavy, outbound-TLS use cases.

---

## Findings

### 1. hashicorp/go-plugin (v1 net/rpc, v2 gRPC)

1. **CGO requirement: none.** `hashicorp/go-plugin` is pure Go. Its gRPC transport uses
   `google.golang.org/grpc` (also pure Go), so the full stack compiles with `CGO_ENABLED=0`.
   [hashicorp/go-plugin README](https://github.com/hashicorp/go-plugin#readme)

2. **Cross-platform support.** Works on Linux, macOS, and Windows × amd64/arm64 via a local TCP or
   Unix socket child-process model. The plugin binary and host binary must target the same OS/ARCH —
   one build per platform, matching the existing `task build:all` matrix.
   [go-plugin Architecture](https://github.com/hashicorp/go-plugin#architecture)

3. **Protocol overhead (streaming).** v1 uses `net/rpc`/`encoding/gob` (Go-only). v2 uses gRPC +
   protobuf. For SSE streaming, neither can pass an `io.ReadCloser` directly across the RPC boundary
   — chunks must be serialized, adding latency and at least one extra copy per event.
   [go-plugin gRPC interface notes](https://github.com/hashicorp/go-plugin/blob/main/docs/guide-plugin-write-non-go.md)

4. **Suitability for streaming HTTP proxying.** The canonical use case (Terraform) is structured
   request/response, not streaming. Bridging `backend.Chat` → server-streaming gRPC requires a
   custom RPC layer and adds measurable latency. **Not recommended for SSE workloads.**

5. **Operational complexity.** N+1 binaries, subprocess lifecycle management, handshake protocol,
   versioning. Significant added complexity for this project.

---

### 2. WASM-based Plugins (Wazero / Extism)

6. **CGO requirement: none.** Wazero is explicitly designed for `CGO_ENABLED=0`.
   [Wazero](https://github.com/tetratelabs/wazero) [Extism Go SDK](https://github.com/extism/go-sdk)

7. **Cross-platform.** Wazero supports all 6 target platforms. WASM bytecode is arch-independent.

8. **WASI networking: critical gap.** WASI Preview 1 (Wazero's supported standard) has **no socket
   or TLS API**. Outbound HTTPS requires either custom host-function trampolines or a
   request/response transform model where the host does all I/O. WASI Preview 2 adds networking but
   is experimental in Wazero.
   [Wazero WASI docs](https://wazero.io/docs/engineering/WASI/)
   [WASI P1 spec](https://github.com/WebAssembly/WASI/blob/main/legacy/preview1/docs.md)

9. **Extism model.** Plugins are sandboxed (bytes in → bytes out). Extism provides
   `extism_http_request` as a host function, but streaming requires buffering through the host
   trampoline. **Not transparent for SSE.**
   [Extism PDK HTTP](https://extism.org/docs/concepts/host-functions/)

10. **Performance.** ~50–200 µs per call overhead under Wazero's compiler backend. Compounds for
    per-chunk SSE processing. No published benchmarks for WASM-mediated HTTP streaming exist.

11. **Maturity.** Wazero 1.x / Extism 1.x stable for transform workloads; not production-tested for
    outbound TLS + token caching + streaming. **Not recommended for this workload.**

---

### 3. Compile-time Module Separation (current architecture extended)

12. **CGO requirement: none.** Ordinary Go packages, fully honoured by `CGO_ENABLED=0`.

13. **Cross-platform.** Identical to the host binary; the existing `task build:all` matrix covers
    all six targets with no extra steps.

14. **How it works.** Providers live in `internal/providers/<name>/`, implement `backend.Backend`,
    and are registered in `internal/cli/serve.go`. This is the existing pattern — zero new
    dependencies, no subprocess lifecycle, no protocol negotiation, no extra copies for streaming.

15. **"Bring your own binary" variation.** External contributors maintain a separate Go module and
    use `go.mod` `replace` directives to overlay their provider into a fork of `llm-proxy`. This is
    how `Caddy xcaddy` and `Terraform plugin-framework` work.
    [xcaddy](https://github.com/caddyserver/xcaddy)
    [Terraform plugin framework](https://developer.hashicorp.com/terraform/plugin/framework)

16. **Limitation.** Third parties distributing closed-source providers must ship source or a binary
    fork. No runtime-loadable drop-in plugins.

---

### 4. Out-of-Process HTTP Sidecar

17. **CGO requirement: none** for the proxy's routing/discovery logic. The sidecar can be any
    language.

18. **Pattern.** Sidecar exposes an OpenAI-compatible HTTP API on localhost. Static config:
    `api_base = "http://127.0.0.1:9001"` in an HCL block — **already supported** by the
    architecture. Dynamic: rendezvous file or local HTTP registry. Supervisor-launched: `os/exec` +
    health-check poll.
    [Go os/exec docs](https://pkg.go.dev/os/exec)

19. **Streaming.** HTTP/1.1 chunked transfer or SSE works transparently over localhost — no extra
    serialization. The existing `*backend.Response` `io.ReadCloser` interface connects naturally.
    **Best streaming story of the four options.**

20. **Health-check patterns.** `GET /healthz` with exponential back-off. Consul/xDS are overkill
    for a single-host deployment.
    [Ollama model server pattern](https://github.com/ollama/ollama/blob/main/docs/api.md)

21. **Operational complexity.** Multiple binaries, port allocation, process management. But unlocks
    independent release cycles and non-Go providers.

---

### Supplementary: Pure-Go gRPC

22. **`google.golang.org/grpc` (grpc-go) is pure Go**, implementing HTTP/2 via
    `golang.org/x/net/http2`. `CGO_ENABLED=0` builds are tested in CI.
    [grpc-go](https://github.com/grpc/grpc-go)

23. **ConnectRPC** (`connectrpc/connect-go`) is a pure-Go framework speaking gRPC, gRPC-Web, and
    Connect over HTTP/1.1 or HTTP/2. No CGO; lighter weight than raw grpc-go.
    [connectrpc/connect-go](https://github.com/connectrpc/connect-go)

---

## Sources

- [hashicorp/go-plugin](https://github.com/hashicorp/go-plugin) — CGO status, architecture,
  streaming limits
- [Wazero](https://wazero.io/) / [tetratelabs/wazero](https://github.com/tetratelabs/wazero) —
  CGO-free status and WASI gaps
- [Extism Go SDK](https://github.com/extism/go-sdk) — outbound HTTP host function
- [WASI Preview 1 spec](https://github.com/WebAssembly/WASI/blob/main/legacy/preview1/docs.md) —
  confirms no socket API
- [grpc-go](https://github.com/grpc/grpc-go) — confirms pure-Go gRPC
- [connectrpc/connect-go](https://github.com/connectrpc/connect-go) — pure-Go gRPC alternative
- [xcaddy](https://github.com/caddyserver/xcaddy) — compile-time plugin build prior art
- [Ollama REST API](https://github.com/ollama/ollama/blob/main/docs/api.md) — HTTP sidecar prior art

---

## Gaps

1. No published benchmark for Wazero-mediated SSE streaming throughput.
2. No prototype for `backend.Chat` → server-streaming gRPC via `go-plugin` v2.
3. WASI Preview 2 GA timeline in Wazero is unclear.
4. HTTP sidecar: Windows-compatible process rendezvous (named pipes vs. TCP) needs verification.
