# IBM Bob: JWT Authorization vs. HMAC Request Signing

> **Scope**: Does JWT (`Authorization: Bearer …`) automatically include HMAC request
> signing behaviour in the `bob` client? Are the two mechanisms coupled or independent?

**Short answer**: They are **independent, orthogonal mechanisms**. A JWT credential causes
`Authorization: Bearer <token>` to be sent; HMAC request signing (`x-request-timestamp` /
`x-request-signature`) is controlled entirely by the **target URL pattern** plus the
presence of an HMAC secret. Both can be present on the same request, either can be absent,
and neither implies the other.

---

## Primary Source: `/opt/homebrew/bin/bob` (minified Node.js bundle)

All code excerpts below are quoted from the current installed `bob` binary. Because the
file is a single-file compiled bundle the identifier names shown are minified; the
semantic meaning is explained inline.

### 1. `GatewayService` constructor — credential detection

```javascript
// Lg() → returns true if running inside a "private preview" / in-tree dev build.
// When false (i.e. normal install), both HMAC secret sources are consulted.
Lg() || (
  this.secretKey = process.env.RUN_TIME_HMAC_SECRET,
  this.secretKey || (this.secretKey = DUn("BUILD_TIME_HMAC_SECRET"))
)
```

- `RUN_TIME_HMAC_SECRET` — operator-supplied env var; takes precedence.
- `BUILD_TIME_HMAC_SECRET` — AES-256-CBC encrypted value baked into the binary;
  `DUn()` decrypts it at runtime using a key derived from the binary path.
- `Lg()` private-preview guard disables signing in the in-tree development build only.
- `this.secretKey` is **unrelated to the credential type** (JWT vs. opaque key); it is
  set in the same constructor branch regardless of what `apiKey` looks like.

**Source**: `GatewayService` constructor, extracted from `/opt/homebrew/bin/bob` ~line
6446448 context.

### 2. `verifyIsNewApiKey` — credential-type detection

```javascript
verifyIsNewApiKey() {
  if (!DU(this.apiKey))  // DU = starts with "sk-" or "pk-" (legacy keys)
    try {
      this.parseJwt(this.apiKey);  // throws if not a valid JWT
      this.isApiKey = false;       // successfully parsed → treat as JWT
    } catch {
      this.isApiKey = true;        // parse failed → opaque API key
    }
}
```

- `isApiKey = false` → credential is a JWT (`Authorization: Bearer …`)
- `isApiKey = true`  → credential is an opaque key (`Authorization: Apikey …`)

No reference to `secretKey` here. Credential detection and signing-key setup are
completely separate.

**Source**: same file, same context block.

### 3. `fetch()` — authorization header and signing headers (the key logic)

```javascript
async fetch(path, opts = {}, signal) {
  // … build URL …
  let headers = new Headers(opts.headers);
  headers.set("Content-Type", "application/json");
  headers.set("User-Agent", `bobshell/${this.bobshellVersion}`);

  // (A) Authorization header — driven by CREDENTIAL TYPE only
  this.apiKey    && headers.set("Authorization", `Bearer ${this.apiKey}`);
  this.isApiKey  && headers.set("Authorization", `Apikey  ${this.apiKey}`);

  // (B) Routing headers — driven by CONFIGURATION only
  this.instanceId && headers.set("x-instance-id", this.instanceId);
  this.teamId     && headers.set("x-team-id",      this.teamId);

  // (C) HMAC request signing — driven by URL PATTERN + SECRET PRESENCE
  if (this.shouldSignUrl(fullUrl) && this.secretKey) {
    let body = this.serializeBody(opts.body);
    let sigHeaders = mUn(method, pathname+search, this.secretKey, body);
    // → { "x-request-timestamp": "...", "x-request-signature": "..." }
    Object.entries(sigHeaders).forEach(([k,v]) => { headers.set(k,v); });
  }
  // … retry loop …
}
```

`shouldSignUrl`:
```javascript
shouldSignUrl(url) {
  let customBase = process.env.CUSTOM_BASE_URL;
  return url.includes(lQ) ||                    // lQ = DEFAULT_BACKEND_URL
         (customBase && url.includes(customBase)) ||
         false;
}
```

The signing guard (`shouldSignUrl && secretKey`) is **entirely URL-based and
secret-based**. It does not inspect `this.isApiKey`, does not check whether the
credential is a JWT, and does not read the JWT payload.

**Source**: same file, `Pc.fetch` method, character offset ~6446448.

### 4. Signing algorithm — `mUn` / `o5s`

```javascript
// o5s = createRequestSignature(method, pathAndQuery, timestamp, body, secret)
function o5s(method, pathAndQuery, timestamp, body = "", secret) {
  let bodyHash = crypto.createHash("sha256").update(body).digest("hex");
  let canonical = `${method.toUpperCase()}\n${pathAndQuery}\n${timestamp}\n${bodyHash}`;
  return crypto.createHmac("sha256", secret).update(canonical).digest("hex");
}

// mUn = createRequestSigningHeaders(method, pathAndQuery, secret, body)
function mUn(method, pathAndQuery, secret, body = "") {
  let timestamp = new Date().toISOString();
  let signature = o5s(method, pathAndQuery, timestamp, body, secret);
  return { "x-request-timestamp": timestamp, "x-request-signature": signature };
}
```

This is identical to the algorithm already implemented in
`internal/providers/ibmbob/signing.go`.

**Source**: same file, character offset ~763, lines containing `createHmac` and
`x-request-timestamp`.

---

## Conclusion

| Question | Answer |
|----------|--------|
| Does a JWT credential trigger request signing? | **No.** Signing is URL-gated, not credential-gated. |
| Can a JWT request also be HMAC-signed? | **Yes.** Both headers coexist when the URL matches and `secretKey` is set. |
| Does a plain `Apikey` request skip signing? | **No.** Signing still fires for matching URLs regardless of credential type. |
| Is the HMAC secret derived from the JWT? | **No.** The secret comes from `RUN_TIME_HMAC_SECRET` env or the binary-embedded `BUILD_TIME_HMAC_SECRET`. |
| Does the JWT payload influence signing? | **No.** Only `method`, `path+query`, `timestamp`, and `SHA-256(body)` enter the canonical string. |

### Implication for `llm-proxy`

The existing `ibmbob` provider in `internal/providers/ibmbob/` already models this
correctly:

- `auth.go:AuthorizationHeader` sets `Authorization: Bearer` for JWTs or
  `Authorization: Apikey` for opaque keys — independent of signing.
- `signing.go:SignRequest` signs the request using the configured `hmac_secret` —
  independent of credential type.

**No changes are required** to align with the behavior observed in the `bob` binary.
The two mechanisms are and should remain independent.

---

## Sources

| Source | What was read |
|--------|--------------|
| `/opt/homebrew/bin/bob` (Node.js bundle) | `GatewayService` constructor (secretKey assignment, verifyIsNewApiKey), `Pc.fetch` method (auth headers + signing guard), `o5s`/`mUn` signing functions |
| `internal/providers/ibmbob/auth.go` | `AuthorizationHeader`, `looksLikeJWT`, `ClassifyKey` |
| `internal/providers/ibmbob/signing.go` | `SignRequest`, `ComputeSignature` |
| `docs/research/ibm-bob-oidc.md` §A.5 | Pre-existing HMAC signing summary (confirmed by direct source inspection) |
