# IBM Bob OIDC Authentication Research

> **Scope**: How pi-dangit-bobby authenticates to IBM Bob's inference backend, gap analysis
> against the existing llm-proxy `ibmbob` provider, and a concrete Go implementation plan.

---

## A. How pi-dangit-bobby Does It

### A.1 Flow Classification

pi-dangit-bobby does **not** use the OIDC client-credentials grant (no `client_id` /
`client_secret` pair, no `/token` endpoint discovery). It uses two completely different
authentication paths:

| Mode | Grant / Flow | Description |
|------|-------------|-------------|
| **Static API key** | n/a (pre-shared secret) | Operator sets `IBM_BOB_API_KEY` or `IBM_BOB_KEY` and pi passes it directly as `Authorization: Apikey <value>` (or `Authorization: Bearer <value>` if `IBM_BOB_AUTH_SCHEME=Bearer`). |
| **Browser SSO** | Authorization Code Flow (OAuth 2.0 §4.1, not OIDC per se) | Pi's `OAuth.login` hook starts a local callback HTTP server, redirects the user to `https://bob.ibm.com/login?callback_uri=…&state=…`, and posts the code to a proprietary IBM Bob token endpoint once the browser completes the round-trip. |

**Summary**: pi-dangit-bobby does _not_ implement classic OIDC discovery or
client-credentials. What it calls "SSO" is an IBM-proprietary Authorization Code flow
against non-standard Bob endpoints (`/authn/v1/auth/token` and `/authn/v1/auth/refresh`).

### A.2 Endpoints Hit

#### Token endpoints (SSO path only)

| Endpoint | Method | Purpose | Source |
|----------|--------|---------|--------|
| `https://bob.ibm.com/login?callback_uri=…&state=…` | Browser redirect | User authenticates; Bob redirects back to `callbackUri` with `?code=…&state=…` | `extensions/bob.ts:loginBob` |
| `<bobOrigin>/authn/v1/auth/token` | POST JSON | Exchanges the one-time `code` for `{ token, refresh_token, expires_in }` | `extensions/bob.ts:postToken("/authn/v1/auth/token", { code })` |
| `<bobOrigin>/authn/v1/auth/refresh` | POST JSON | Exchanges a `refresh_token` for a new `{ token, refresh_token }` | `extensions/bob.ts:postToken("/authn/v1/auth/refresh", { refresh_token })` |
| `<base_url>/model/info` | GET | Post-auth model catalog discovery | `extensions/bob.ts:fetchBobModelCatalog` |
| `<base_url>/chat/completions` (and analogues) | POST | Inference | `extensions/bob.ts:streamBob` → delegates to Pi adapters |

`bobOrigin` defaults to `https://api.us-east.bob.ibm.com` (derived from the configured
`IBM_BOB_BASE_URL` via `new URL(…).origin`).
— Source: `extensions/bob.ts:bobOrigin()` and `extensions/bob.ts:const DEFAULT_BOB_ORIGIN`.

#### No OIDC Discovery URL

There is no `.well-known/openid-configuration` fetch, no `jwks_uri` lookup, and no
`id_token` validation. The token is an opaque JWT whose expiry is parsed from its payload
(`exp` claim × 1000 − 5-minute buffer) for local TTL management only.
— Source: `extensions/bob.ts:jwtExpiry()`.

### A.3 Credentials and Config Required

**API key path** (non-interactive / CI):

| Config | Source in pi-dangit-bobby | Notes |
|--------|--------------------------|-------|
| `IBM_BOB_API_KEY` / `IBM_BOB_KEY` | env var | Static opaque API key; uses `Authorization: Apikey <val>` by default |
| `IBM_BOB_AUTH_SCHEME` | env var, default `Apikey` | Override to `Bearer` if the credential is a JWT |
| `IBM_BOB_BASE_URL` | env var, default `https://api.us-east.bob.ibm.com/inference/v1` | Upstream inference base URL |
| `IBM_BOB_INSTANCE_ID` / `IBM_BOB_TEAM_ID` | env var _or_ `~/.bob/settings.json` `ibm.instanceId`/`ibm.teamId` | Routing headers; optional |
| `IBM_BOB_REQUEST_SIGNING_SECRET` | env var | HMAC-SHA256 key ≥ 32 bytes; optional but fail-closed when `IBM_BOB_REQUIRE_REQUEST_SIGNING=true` |

**SSO path** (interactive):

Same infrastructure as above except the access token is obtained at runtime via the
Authorization Code flow and stored (along with `refresh_token`) in Pi's
`~/.pi/agent/auth.json` via Pi's `OAuthCredentials` store. Token request timeout
defaults to 10 s (`IBM_BOB_TOKEN_REQUEST_TIMEOUT_MS`).
— Source: `extensions/bob.ts:loginBob`, `extensions/bob.ts:refreshBobToken`,
`extensions/bob.ts:credentialsFromTokenResponse`.

### A.4 Token Caching and Refresh Strategy

The caching and refresh lifecycle lives inside Pi's OAuth infrastructure; pi-dangit-bobby
contributes three pieces:

1. **Expiry calculation** (`jwtExpiry`): reads `exp` from the JWT payload, converts to
   milliseconds, subtracts a 5-minute buffer. Falls back to `expires_at - 5 min` if present,
   then to `Date.now() + (expires_in ?? 55*60) * 1000 - 5 min`.
   — Source: `extensions/bob.ts:credentialsFromTokenResponse` (~line 520).

2. **Refresh hook** (`oauth.refreshToken → refreshBobToken`): POSTs
   `{ refresh_token }` to `/authn/v1/auth/refresh`, parses the new
   `{ token, refresh_token }`, re-attaches the model catalog, and returns fresh
   `OAuthCredentials`. Pi calls this hook automatically when the stored token is
   approaching expiry.
   — Source: `extensions/bob.ts:refreshBobToken` (~line 548).

3. **Model catalog caching**: The catalog (`BobDiscoveredModel[]`) is JSON-serialised
   into the `OAuthCredentials.bobModelCatalog` extra field so it survives process
   restarts via Pi's auth store. On each token refresh the catalog is re-fetched; on
   failure the previously cached copy from `credentials.bobModelCatalog` is preserved.
   — Source: `extensions/bob.ts:attachBobModelCatalog`, `extensions/bob.ts:cachedCatalog`.

### A.5 HMAC Request Signing

This is the same mechanism llm-proxy already implements. For completeness:

| Header | Value |
|--------|-------|
| `x-request-timestamp` | ISO-8601 UTC timestamp, millisecond precision |
| `x-request-signature` | `HMAC-SHA256(UPPER_METHOD + "\n" + path+query + "\n" + timestamp + "\n" + SHA256(body), secret)` hex |

The secret is read from `IBM_BOB_REQUEST_SIGNING_SECRET` (≥ 32 bytes, no leading/trailing
whitespace). Any existing `x-request-timestamp` / `x-request-signature` headers are
stripped before fresh values are added (`withRequestSigningHeaders`).
— Source: `extensions/request-signing.ts` (entire file).

### A.6 Key Code Paths with File:Line Citations

| Action | File | Function | Approx line |
|--------|------|----------|-------------|
| Detect credential type (JWT vs Apikey) | `extensions/bob.ts` | `jwtPayload` / `applyBobAuthHeaders` | ~370, ~390 |
| Build `Authorization` header | `extensions/bob.ts` | `applyBobAuthHeaders` | ~382 |
| Start callback server for SSO | `extensions/bob.ts` | `startCallbackServer` | ~460 |
| Exchange code for access token | `extensions/bob.ts` | `loginBob` → `postToken("/authn/v1/auth/token", …)` | ~530 |
| Refresh token | `extensions/bob.ts` | `refreshBobToken` → `postToken("/authn/v1/auth/refresh", …)` | ~548 |
| Compute expiry from JWT / response fields | `extensions/bob.ts` | `jwtExpiry` / `credentialsFromTokenResponse` | ~360, ~520 |
| Sign outbound request headers | `extensions/bob.ts` | `signHeaders` → `withRequestSigningHeaders` | ~185 |
| Build canonical request string | `extensions/request-signing.ts` | `createRequestSignature` | ~60 |
| Produce `x-request-timestamp` + `x-request-signature` | `extensions/request-signing.ts` | `createRequestSigningHeaders` | ~68 |
| Merge signing headers into existing header map | `extensions/request-signing.ts` | `withRequestSigningHeaders` | ~77 |

---

## B. Gap Analysis for llm-proxy

### B.1 What the Current `ibmbob` Provider Already Does

The Go provider at `internal/providers/ibmbob/` already handles all three credential
shapes pi-dangit-bobby supports:

| Capability | Go file | Status |
|-----------|---------|--------|
| `Apikey` / `Bearer` scheme selection by key shape | `auth.go:AuthorizationHeader` | ✅ implemented |
| JWT key detection | `auth.go:looksLikeJWT`, `auth.go:ClassifyKey` | ✅ implemented |
| Legacy `sk-`/`pk-` key routing to staging backend | `auth.go:ClassifyKey`, `auth.go:BaseURLFor` | ✅ implemented |
| HMAC-SHA256 request signing (`x-request-timestamp`, `x-request-signature`) | `signing.go` | ✅ implemented |
| Identity bootstrap (`/admin/v1/profile`) with 30-min TTL cache | `ibmbob.go:ensureIdentity`, `client.go:fetchIdentity` | ✅ implemented |
| `x-instance-id` / `x-team-id` header injection | `client.go:setHeaders` | ✅ implemented |
| Retry on 5xx, 408, 429, transient body markers | `client.go:shouldRetryResponse` | ✅ implemented |
| Budget reporting | `budget.go` | ✅ implemented |
| Upstream model catalog discovery | `models.go` | ✅ implemented |

### B.2 What Is Missing or Different

| Gap | Severity | Detail |
|-----|----------|--------|
| **Browser SSO / Authorization Code login** | High | pi-dangit-bobby's `/login ibm-bob` starts a local HTTP callback server, opens the Bob SSO browser page, exchanges the one-time code against `/authn/v1/auth/token`, and persists the resulting JWT + refresh token. llm-proxy has no equivalent for IBM Bob; operators must supply a pre-obtained JWT or API key via HCL. |
| **Token refresh** | High | No `POST /authn/v1/auth/refresh` call exists. If the operator-supplied JWT expires the proxy returns 401s until the operator rotates the value manually. |
| **JWT expiry awareness** | Medium | pi-dangit-bobby parses `exp` from the JWT payload to compute a 5-minute pre-emptive refresh deadline. llm-proxy uses the credential verbatim until an upstream 401 occurs; it then fails rather than auto-refreshing. |
| **On-disk SSO token persistence** | Medium | pi-dangit-bobby stores `{ access, refresh, expires }` in Pi's auth store across restarts. llm-proxy has no per-backend IBM Bob token file; state is entirely in-process (or re-injected at start via env). |
| **Auth scheme override for env keys** | Low | pi-dangit-bobby's `IBM_BOB_AUTH_SCHEME=Bearer` allows an env key to be sent as `Bearer` rather than `Apikey`. llm-proxy infers the scheme from key shape (`looksLikeJWT` → Bearer, opaque → Apikey), which covers the common cases without an explicit override field. |
| **Fail-closed signing enforcement** | Low | pi-dangit-bobby's `IBM_BOB_REQUIRE_REQUEST_SIGNING=true` refuses to start if the secret is absent. llm-proxy's `New()` already returns an error when `hmac_secret` is absent and the key routes to the modern backend (`ibmbob.go: if authn && s.HMACSecret == ""`), which is functionally equivalent. |

**Key clarification on "OIDC"**: The task title references OIDC, but pi-dangit-bobby does
_not_ use OIDC (no discovery URL, no `id_token`, no standard grant type, no `scope`
parameter in token requests). The IBM Bob SSO flow is a proprietary Authorization Code
flow against IBM-specific token endpoints. If IBM exposes a separate OIDC
client-credentials endpoint, it is not visible in pi-dangit-bobby and would require a
different implementation. The plan below implements what pi-dangit-bobby actually does.

---

## C. Go Implementation Plan

### C.1 Packages to Use

**No new external dependencies are required.** The complete flow uses only:

| Package | Purpose | In `go.mod`? |
|---------|---------|-------------|
| `net/http` | Token exchange POST, local callback server | stdlib |
| `encoding/json` | Parse `{ token, refresh_token, expires_in, expires_at }` | stdlib |
| `encoding/base64` + `encoding/json` | JWT `exp` extraction (pattern already in `auth.go:looksLikeJWT`) | stdlib |
| `crypto/rand` | Generate 16-byte `state` nonce | stdlib |
| `sync` | Token cache mutex | stdlib |
| `time` | Expiry arithmetic | stdlib |
| `github.com/thelonelyghost/llm-proxy/internal/paths` | `WriteSecret`, `CacheDir` for on-disk persistence | internal |

If a full OIDC discovery + JWT-signature verification path is needed in the future,
`golang.org/x/oauth2` and `github.com/coreos/go-oidc/v3` would be the standard
additions. **Neither is currently in `go.mod`**, so they would require `go get` plus
`go.mod` / `go.sum` updates. For the Authorization Code flow pi-dangit-bobby actually
uses, they are unnecessary.

### C.2 Where the New Code Lives

```
internal/providers/ibmbob/
├── ibmbob.go      (existing — extend Settings; wire ssoCached + ensureToken)
├── auth.go        (existing — KeyKind, ClassifyKey, AuthorizationHeader, …)
├── signing.go     (existing — HMAC helpers)
├── client.go      (existing — forward, setHeaders, fetchIdentity, …)
├── budget.go      (existing)
├── models.go      (existing)
├── sso.go         (NEW — SSOToken, SSOTokenStore, jwtExpiry,
│                          loginBob, refreshSSOToken, callbackServer)
└── sso_test.go    (NEW — unit tests via httptest.Server)
```

A shared `internal/oidc/` package is **not** recommended:

1. No other provider uses IBM Bob's proprietary token endpoints.
2. The flow is non-standard (no PKCE, no `/.well-known`, no `client_secret`).
3. The repo convention is provider-local auth code — see
   `internal/providers/copilot/auth.go` as the precedent.

### C.3 Config Struct Additions (HCL Fields)

Add two optional fields to `Settings` in `ibmbob.go`:

```go
// SSOAuthBase overrides the origin used for SSO token endpoints.
// Defaults to the origin derived from api_base, falling back to
// https://api.us-east.bob.ibm.com. Only needed when the Bob deployment
// uses a separate auth origin.
SSOAuthBase string `hcl:"sso_auth_base,optional"`

// SSOClientID is forwarded to the token endpoint when the Bob SSO
// server begins requiring a client identifier. Leave empty for the
// current production endpoint (which accepts no client_id).
SSOClientID string `hcl:"sso_client_id,optional"`
```

**No `oidc_issuer`, `client_secret`, or `scopes` fields** are required because the
IBM Bob SSO token exchange currently sends only `{ "code": … }` with no client
credentials or scope parameters (verified from `bob.ts:postToken` — it marshals only
the supplied body argument with no additional fields).

On-disk token path: `<paths.CacheDir()>/ibmbob_<backendName>_token.json` (mode 0600
via `paths.WriteSecret`), analogous to the existing `copilot_token.json`.

### C.4 Token Fetch and Cache Strategy

Model the store after `internal/providers/copilot/auth.go`:

```go
// SSOToken mirrors the IBM Bob /authn/v1/auth/token response fields
// that matter for caching and refresh.
type SSOToken struct {
    AccessToken  string    `json:"access_token"`
    RefreshToken string    `json:"refresh_token,omitempty"`
    ExpiresAt    time.Time `json:"expires_at"`
}

// Expired reports whether the token should be refreshed.
// Uses a 5-minute pre-emptive buffer, matching pi-dangit-bobby's jwtExpiry.
func (t *SSOToken) Expired(now time.Time) bool {
    return now.Add(5 * time.Minute).After(t.ExpiresAt)
}
```

**Two-level cache** (in-memory + on-disk), same pattern as Copilot:

```
ensureToken(ctx)
  ├─ ssoCached != nil && !Expired(now)  →  return fast path
  ├─ load disk cache; if valid          →  populate ssoCached, return
  ├─ disk token expired + RefreshToken present
  │    →  refreshSSOToken(ctx, refreshToken)
  │    →  paths.WriteSecret(tokenPath, json)
  │    →  return
  └─ no usable token
       →  return fmt.Errorf("… run `llm-proxy ibm-bob login`")
```

`Provider` gains:

```go
ssoMu     sync.Mutex
ssoCached *SSOToken
tokenPath string   // resolved once in New()
```

### C.5 HTTP Transport Wrapping Pattern

No `http.RoundTripper` wrapper. The SSO access token feeds directly into the existing
`setHeaders` call in `client.go`:

```go
func (p *Provider) setHeaders(req *http.Request, pathAndQuery string, body []byte) {
    // … Content-Type / Accept / User-Agent unchanged …

    // SSO access token takes precedence over the static api_key.
    p.ssoMu.Lock()
    sso := p.ssoCached
    p.ssoMu.Unlock()

    var credential string
    var kind KeyKind
    if sso != nil {
        credential = sso.AccessToken
        kind = KeyKindJWT  // SSO tokens are always JWTs → Bearer scheme
    } else {
        credential = p.settings.APIKey
        kind = p.keyKind
    }
    req.Header.Set("Authorization", AuthorizationHeader(credential, kind))

    // … existing identity headers and HMAC signing logic unchanged …
}
```

A `RoundTripper` is intentionally avoided: HMAC signing (in `signing.go`) requires the
request body bytes before the request is dispatched, and the existing `httplog.WrapClient`
debug transport must remain the outermost layer.

### C.6 Login CLI Subcommand

Add `llm-proxy ibm-bob login` alongside the existing `llm-proxy ibm-bob extract`:

**File**: `internal/cli/ibmbob.go` (existing file, new subcommand).

Flow:

1. Call `sso.StartCallbackServer(ctx)` → binds a random local TCP port, returns
   `{ callbackURI, codeCh <-chan string, close() }`.
2. Construct the login URL:
   `<ssoAuthBase>/login?callback_uri=<callbackURI>&state=<hex(rand16)>`
   and print it (or open the browser if a `-b` flag is provided).
3. `select { case code := <-codeCh: … case <-ctx.Done(): … case <-timeout: … }`.
4. POST `{ "code": code }` to `<ssoAuthBase>/authn/v1/auth/token`
   with `Content-Type: application/json` and `User-Agent: <settings.UserAgent>`.
   No `Authorization` header on the token endpoint call (matches `bob.ts:postToken`).
5. Parse the response into `SSOToken`; compute `ExpiresAt` via `jwtExpiry(token)`,
   falling back to `resp.ExpiresAt` (epoch ms) then `time.Now() + resp.ExpiresIn`.
6. Persist to `tokenPath` via `paths.WriteSecret`.
7. Print confirmation to stdout.

### C.7 Token Refresh Flow

```go
// refreshSSOToken implements POST /authn/v1/auth/refresh.
// It mirrors bob.ts:postToken("/authn/v1/auth/refresh", { refresh_token }).
func (p *Provider) refreshSSOToken(ctx context.Context, refreshToken string) (*SSOToken, error) {
    payload, _ := json.Marshal(map[string]string{"refresh_token": refreshToken})
    req, err := http.NewRequestWithContext(ctx, http.MethodPost,
        p.ssoAuthBase+"/authn/v1/auth/refresh",
        bytes.NewReader(payload))
    if err != nil {
        return nil, fmt.Errorf("build refresh request: %w", err)
    }
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("User-Agent", p.settings.UserAgent)
    // No Authorization header — token endpoint is unauthenticated.

    resp, err := p.client.Do(req)
    // … read body, check status, parse into bobTokenResponse,
    //    call jwtExpiry / falls back, return *SSOToken …
}
```

`jwtExpiry` is a private helper in `sso.go` that reuses the existing base64+JSON decode
pattern from `auth.go:looksLikeJWT`:

```go
func jwtExpiry(token string) (time.Time, bool) {
    parts := strings.Split(token, ".")
    if len(parts) < 2 {
        return time.Time{}, false
    }
    raw, err := base64.RawURLEncoding.DecodeString(parts[1])
    if err != nil {
        return time.Time{}, false
    }
    var claims struct {
        Exp int64 `json:"exp"`
    }
    if err := json.Unmarshal(raw, &claims); err != nil || claims.Exp == 0 {
        return time.Time{}, false
    }
    // 5-minute pre-emptive buffer, matching pi-dangit-bobby's jwtExpiry.
    return time.Unix(claims.Exp, 0).Add(-5 * time.Minute), true
}
```

### C.8 Interaction with the Existing `backend.Backend` Interface

No interface changes are required. `SSOToken` is an internal implementation detail:

- `New()` optionally loads a persisted `SSOToken` from disk (non-fatal when absent;
  the operator may be using a static `api_key` only).
- `Chat`, `Complete`, `Embed`, `Responses`, and `Budget` each call `ensureToken(ctx)`
  as the first step, before the existing `ensureIdentity(ctx)`.
- A new `Login(ctx context.Context, onURL func(string)) error` method is added on
  `*Provider` for the CLI. It is not part of `backend.Backend`; the CLI accesses it
  via a concrete `*ibmbob.Provider` reference, matching the pattern used by
  `copilot.Provider.Auth()` for the Copilot login subcommand.

### C.9 Documentation Updates Required

Per `AGENTS.md` §4, the following files must be updated alongside the code:

**`docs/providers/ibm-bob.md`**:
- Add `sso_auth_base` and `sso_client_id` to the optional configuration table.
- Add a new "Login / SSO" section documenting `llm-proxy ibm-bob login`, the token
  file location, and automatic refresh behaviour.
- Update the "API-key routing" / "Required configuration" preamble to mention that
  JWT credentials obtained via SSO can replace a static `api_key`.

**`docs/configuration.md`**:
- Add `<user-cache-dir>/llm-proxy/ibmbob_<name>_token.json` (mode 0600) to the
  on-disk state table.

### C.10 Test Coverage

Following `AGENTS.md` §§2–3 (80% coverage, tests for every change):

**`internal/providers/ibmbob/sso_test.go`** (new):
- `TestJWTExpiry_Valid` / `TestJWTExpiry_MissingExp` — unit-test `jwtExpiry`.
- `TestRefreshSSOToken_Success` — `httptest.Server` returning a valid refresh response.
- `TestRefreshSSOToken_NetworkError` — confirm error wrapping.
- `TestEnsureToken_InMemoryHit` — confirm no disk or network I/O on cache hit.
- `TestEnsureToken_DiskHit` — seed a token file; confirm it is loaded and set in memory.
- `TestEnsureToken_Expired_Refresh` — seed an expired token + refresh token; confirm
  `refreshSSOToken` is called and the new token is persisted.
- `TestEnsureToken_NoCreds_ReturnsError` — confirm `ErrNotAuthenticated`-style error.

**`internal/providers/ibmbob/provider_test.go`** (extend existing):
- `TestNew_SSOTokenLoadedFromDisk` — verify `New()` picks up a pre-seeded cache file.
- `TestForward_SSOTokenUsed` — verify that when both `api_key` and an in-memory SSO
  token are present, the SSO token's `Authorization: Bearer …` header is used.

**Fuzz** (optional, low priority):
- `TestFuzzJWTExpiry` in `sso_fuzz_test.go` exercising the base64+JSON decode path,
  following the pattern in `internal/config/fuzz_test.go`.

---

## Sources

### Kept

- **`extensions/bob.ts`** (`pi-dangit-bobby`) — primary source for auth flow, SSO callback, token endpoint calls, credential handling, JWT expiry logic, and model catalog caching.
- **`extensions/request-signing.ts`** (`pi-dangit-bobby`) — canonical HMAC signing algorithm; confirms algorithm identity with llm-proxy's `signing.go`.
- **`extensions/signed-openai.ts`** (`pi-dangit-bobby`) — shows how signing headers are injected into the delegated streaming path.
- **`extensions/bob-version.ts`** (`pi-dangit-bobby`) — User-Agent construction; version discovery.
- **`README.md`** (`pi-dangit-bobby`) — configuration table, smoke-test evidence, auth scheme documentation.
- **`internal/providers/ibmbob/ibmbob.go`** — `Settings`, `Provider`, `New()`, `ensureIdentity`.
- **`internal/providers/ibmbob/auth.go`** — `ClassifyKey`, `AuthorizationHeader`, `BaseURLFor`, `IsAuthnBackend`, `shouldSignURL`.
- **`internal/providers/ibmbob/signing.go`** — `SignRequest`, `ComputeSignature`, `CanonicalString`.
- **`internal/providers/ibmbob/client.go`** — `forward`, `setHeaders`, `fetchIdentity`, retry logic.
- **`internal/providers/ibmbob/budget.go`** — profile-fetch pattern reused by `fetchProfileBody`.
- **`internal/providers/copilot/auth.go`** — precedent for two-level token cache, `FileTokenStore`, `Authenticator` design.
- **`internal/paths/paths.go`** — `WriteSecret`, `CacheDir`, `ConfigDir`.
- **`docs/providers/ibm-bob.md`** — current provider docs; identifies what to update.
- **`docs/configuration.md`** — on-disk state table, HCL schema reference.
- **`go.mod`** — confirms `golang.org/x/oauth2` and `go-oidc` are not present.

### Dropped

None. All consulted sources are listed above. The GitHub raw API was not reachable from
this agent; the supervisor provided a local clone of pi-dangit-bobby.

---

## Gaps

1. **Exact `/authn/v1/auth/token` contract**: The code sends `{ "code": code }` and
   expects `{ token, refresh_token?, expires_in?, expires_at? }`. The endpoint is not
   publicly documented. If IBM later requires PKCE (`code_verifier`) or a `client_id`,
   both `loginBob` and `sso.go` would need to be updated.

2. **Whether the token endpoint requires a `client_id`**: pi-dangit-bobby sends no
   `client_id` today. The `sso_client_id` HCL field is included in the plan as an
   optional forward-compatibility escape hatch; leave it empty in the default config.

3. **True OIDC**: The task title references OIDC, but pi-dangit-bobby does not use it.
   If IBM Bob exposes an OIDC client-credentials endpoint (for machine-to-machine use
   without a browser), that path is not visible in pi-dangit-bobby and would require a
   separate implementation using `golang.org/x/oauth2/clientcredentials` plus OIDC
   discovery. **Next step**: request IBM Bob internal documentation for any
   `.well-known/openid-configuration`-based `client_credentials` grant.

4. **Scopes**: No `scope` parameter appears in pi-dangit-bobby's token exchange calls.
   If the IBM Bob SSO server begins requiring one, it would need to be added to the
   `sso.go:loginBob` POST body.
