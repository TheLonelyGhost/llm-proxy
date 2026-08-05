package ibmbob_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thelonelyghost/llm-proxy/internal/providers/ibmbob"
)

// makeJWTWithExp builds a minimal JWT whose payload contains {"exp": exp}.
func makeJWTWithExp(exp int64) string {
	payload, _ := json.Marshal(map[string]any{"exp": exp, "sub": "test"})
	encoded := encodeBase64URL(payload)
	return "header." + encoded + ".sig"
}

func encodeBase64URL(b []byte) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	_ = alphabet
	// Use standard library via encoding/base64 inside the main package tests.
	// We import it here via the helper in auth_test.go (makeJWT) indirectly.
	// Simplest: just re-encode manually.
	import64 := func(data []byte) string {
		const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
		var buf strings.Builder
		for i := 0; i < len(data); i += 3 {
			b0 := data[i]
			var b1, b2 byte
			if i+1 < len(data) {
				b1 = data[i+1]
			}
			if i+2 < len(data) {
				b2 = data[i+2]
			}
			buf.WriteByte(chars[b0>>2])
			buf.WriteByte(chars[(b0&3)<<4|b1>>4])
			if i+1 < len(data) {
				buf.WriteByte(chars[(b1&0xf)<<2|b2>>6])
			}
			if i+2 < len(data) {
				buf.WriteByte(chars[b2&0x3f])
			}
		}
		return buf.String()
	}
	return import64(b)
}

// --- jwtExpiry ---

func TestJWTExpiry_Valid(t *testing.T) {
	const expUnix = int64(1_000_000)
	tok := makeJWTWithExp(expUnix)
	got, ok := ibmbob.JWTExpiry(tok)
	if !ok {
		t.Fatal("expected ok=true for valid JWT with exp")
	}
	want := time.Unix(expUnix-300, 0) // 5-min buffer
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestJWTExpiry_MissingExp(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{"sub": "test"})
	encoded := encodeBase64URL(payload)
	tok := "header." + encoded + ".sig"
	_, ok := ibmbob.JWTExpiry(tok)
	if ok {
		t.Fatal("expected ok=false for missing exp")
	}
}

func TestJWTExpiry_NotJWT(t *testing.T) {
	_, ok := ibmbob.JWTExpiry("not-a-jwt")
	if ok {
		t.Fatal("expected ok=false for non-JWT string")
	}
}

func TestJWTExpiry_InvalidBase64(t *testing.T) {
	_, ok := ibmbob.JWTExpiry("header.!!!.sig")
	if ok {
		t.Fatal("expected ok=false for invalid base64 payload")
	}
}

func TestJWTExpiry_NotJSON(t *testing.T) {
	encoded := encodeBase64URL([]byte("not-json"))
	_, ok := ibmbob.JWTExpiry("header." + encoded + ".sig")
	if ok {
		t.Fatal("expected ok=false for non-JSON payload")
	}
}

// --- SSOToken.Expired ---

func TestSSOToken_Expired(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	// Token exactly at now: within the 5-min buffer → expired.
	tok := &ibmbob.SSOToken{ExpiresAt: now}
	if !tok.Expired(now) {
		t.Error("token at ExpiresAt==now should be expired (inside 5-min buffer)")
	}
	// Token 6 minutes in the future: outside the 5-min buffer → not expired.
	tok2 := &ibmbob.SSOToken{ExpiresAt: now.Add(6 * time.Minute)}
	if tok2.Expired(now) {
		t.Error("token 6 min in future should not be expired")
	}
}

// --- RefreshSSOToken (via EnsureSSO with an expired disk token) ---

func TestRefreshSSOToken_Success(t *testing.T) {
	futureExp := time.Now().Add(1 * time.Hour).Unix()
	respToken := makeJWTWithExp(futureExp)

	var refreshCalled int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/authn/v1/auth/refresh":
			refreshCalled++
			if r.Header.Get("Authorization") != "" {
				http.Error(w, "unexpected Authorization header on token endpoint", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"token":         respToken,
				"refresh_token": "new-refresh",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tokenDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", tokenDir)

	// Seed an expired token with a refresh token.
	expired := &ibmbob.SSOToken{
		AccessToken:  "old-token",
		RefreshToken: "old-refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
	}
	tokenPath := filepath.Join(tokenDir, "ibmbob_bob_token.json")
	writeJSON(t, tokenPath, expired)

	block := buildBlock(t, fmt.Sprintf("hmac_secret = \"topsecret\"\napi_base = %q", srv.URL))
	prov, err := ibmbob.New(block, ibmbob.Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := prov.EnsureSSO(context.Background()); err != nil {
		t.Fatalf("EnsureSSO: %v", err)
	}

	if refreshCalled != 1 {
		t.Errorf("refresh endpoint called %d times, want 1", refreshCalled)
	}
	if got := prov.CurrentAccessToken(); got != respToken {
		t.Errorf("in-memory token = %q, want %q", got, respToken)
	}

	// Refreshed token must also be persisted to disk.
	var diskTok ibmbob.SSOToken
	readJSON(t, tokenPath, &diskTok)
	if diskTok.AccessToken != respToken {
		t.Errorf("disk access_token = %q, want %q", diskTok.AccessToken, respToken)
	}
	if diskTok.RefreshToken != "new-refresh" {
		t.Errorf("disk refresh_token = %q, want new-refresh", diskTok.RefreshToken)
	}
}

func TestRefreshSSOToken_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorised", http.StatusUnauthorized)
	}))
	defer srv.Close()

	tokenDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", tokenDir)

	expired := &ibmbob.SSOToken{
		AccessToken:  "old",
		RefreshToken: "old-refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
	}
	tokenPath := filepath.Join(tokenDir, "ibmbob_bob_token.json")
	writeJSON(t, tokenPath, expired)

	block := buildBlock(t, fmt.Sprintf("hmac_secret = \"topsecret\"\napi_base = %q", srv.URL))
	prov, err := ibmbob.New(block, ibmbob.Options{})
	if err != nil {
		t.Fatal(err)
	}

	err = prov.EnsureSSO(context.Background())
	if err == nil {
		t.Fatal("expected error from HTTP 401 on refresh")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error %q should mention 401", err.Error())
	}
}

func TestRefreshSSOToken_NetworkError(t *testing.T) {
	// Bind a server then immediately close it to simulate a connection refusal.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	srvURL := srv.URL
	srv.Close() // closed before the request fires

	tokenDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", tokenDir)

	expired := &ibmbob.SSOToken{
		AccessToken:  "old",
		RefreshToken: "old-refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
	}
	tokenPath := filepath.Join(tokenDir, "ibmbob_bob_token.json")
	writeJSON(t, tokenPath, expired)

	block := buildBlock(t, fmt.Sprintf("hmac_secret = \"topsecret\"\napi_base = %q", srvURL))
	prov, err := ibmbob.New(block, ibmbob.Options{})
	if err != nil {
		t.Fatal(err)
	}

	err = prov.EnsureSSO(context.Background())
	if err == nil {
		t.Fatal("expected network error")
	}
}

// --- ensureSSO cache levels ---

func TestEnsureSSO_InMemoryHit(t *testing.T) {
	tokenDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", tokenDir)

	// Port 1 will be refused; we must not reach the network.
	block := buildBlock(t, "hmac_secret = \"topsecret\"\napi_base = \"http://127.0.0.1:1\"")
	prov, err := ibmbob.New(block, ibmbob.Options{Now: fixedNow()})
	if err != nil {
		t.Fatal(err)
	}

	// Seed in-memory cache directly.
	future := &ibmbob.SSOToken{
		AccessToken: "mem-token",
		ExpiresAt:   time.Now().Add(1 * time.Hour),
	}
	prov.SetCachedSSO(future)

	if err := prov.EnsureSSO(context.Background()); err != nil {
		t.Fatalf("EnsureSSO with in-memory hit: %v", err)
	}
	if got := prov.CurrentAccessToken(); got != "mem-token" {
		t.Errorf("token = %q, want mem-token", got)
	}
}

func TestEnsureSSO_DiskHit(t *testing.T) {
	tokenDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", tokenDir)

	valid := &ibmbob.SSOToken{
		AccessToken: "disk-token",
		ExpiresAt:   time.Now().Add(1 * time.Hour),
	}
	tokenPath := filepath.Join(tokenDir, "ibmbob_bob_token.json")
	writeJSON(t, tokenPath, valid)

	block := buildBlock(t, "hmac_secret = \"topsecret\"\napi_base = \"http://127.0.0.1:1\"")
	prov, err := ibmbob.New(block, ibmbob.Options{})
	if err != nil {
		t.Fatal(err)
	}

	// Token should have been pre-loaded at New() time.
	if got := prov.CurrentAccessToken(); got != "disk-token" {
		t.Errorf("pre-loaded token = %q, want disk-token", got)
	}

	// EnsureSSO is a no-op (in-memory hit).
	if err := prov.EnsureSSO(context.Background()); err != nil {
		t.Fatalf("EnsureSSO with disk hit: %v", err)
	}
}

func TestEnsureSSO_Expired_NoRefreshToken(t *testing.T) {
	tokenDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", tokenDir)

	expired := &ibmbob.SSOToken{
		AccessToken: "stale",
		ExpiresAt:   time.Now().Add(-1 * time.Hour),
		// RefreshToken deliberately absent.
	}
	tokenPath := filepath.Join(tokenDir, "ibmbob_bob_token.json")
	writeJSON(t, tokenPath, expired)

	block := buildBlock(t, "hmac_secret = \"topsecret\"\napi_base = \"http://127.0.0.1:1\"")
	prov, err := ibmbob.New(block, ibmbob.Options{})
	if err != nil {
		t.Fatal(err)
	}

	err = prov.EnsureSSO(context.Background())
	if err == nil {
		t.Fatal("expected ErrSSOExpired for expired token with no refresh")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("error %q should mention expired", err.Error())
	}
}

func TestEnsureSSO_NoCredentials(t *testing.T) {
	tokenDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", tokenDir)
	// No token file seeded. SSO mode with no credentials.
	block := buildBlock(t, "hmac_secret = \"topsecret\"\napi_base = \"http://127.0.0.1:1\"")
	prov, err := ibmbob.New(block, ibmbob.Options{})
	if err != nil {
		t.Fatal(err)
	}
	err = prov.EnsureSSO(context.Background())
	if err == nil {
		t.Fatal("expected ErrSSOExpired when no token file exists")
	}
}

// --- Callback server ---

func TestCallbackServer_ReceivesCode(t *testing.T) {
	cs, err := ibmbob.StartTestCallbackServer(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	redirectURL := cs.CallbackURI + "?code=mycode&state=" + cs.State
	resp, err := http.Get(redirectURL) //nolint:noctx
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("nil response from callback server")
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("callback status = %d, want 200", resp.StatusCode)
	}

	select {
	case code := <-cs.CodeCh:
		if code != "mycode" {
			t.Errorf("code = %q, want mycode", code)
		}
	default:
		t.Fatal("no code received on CodeCh")
	}
}

func TestCallbackServer_RejectsWrongState(t *testing.T) {
	cs, err := ibmbob.StartTestCallbackServer(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	redirectURL := cs.CallbackURI + "?code=mycode&state=wrongstate"
	resp, err := http.Get(redirectURL) //nolint:noctx
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("nil response from callback server")
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("callback status = %d, want 400 for wrong state", resp.StatusCode)
	}

	select {
	case code := <-cs.CodeCh:
		t.Errorf("should not receive code %q for wrong state", code)
	default:
		// correct: nothing on the channel
	}
}

func TestCallbackServer_RejectsMissingCode(t *testing.T) {
	cs, err := ibmbob.StartTestCallbackServer(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	redirectURL := cs.CallbackURI + "?state=" + cs.State // no code
	resp, err := http.Get(redirectURL)                   //nolint:noctx
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("nil response from callback server")
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for missing code", resp.StatusCode)
	}
}

// --- helpers ---

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

// --- ExchangeCode ---

func TestExchangeCode_Success(t *testing.T) {
	futureExp := time.Now().Add(1 * time.Hour).Unix()
	respToken := makeJWTWithExp(futureExp)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/authn/v1/auth/token" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" {
			http.Error(w, "unexpected Authorization header", http.StatusBadRequest)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["code"] != "test-code" {
			http.Error(w, "wrong code", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token":         respToken,
			"refresh_token": "refresh-abc",
		})
	}))
	defer srv.Close()

	tokenDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", tokenDir)

	block := buildBlock(t, fmt.Sprintf("hmac_secret = \"topsecret\"\napi_base = %q", srv.URL))
	prov, err := ibmbob.New(block, ibmbob.Options{})
	if err != nil {
		t.Fatal(err)
	}

	tok, err := prov.ExchangeCode(context.Background(), "test-code")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if tok.AccessToken != respToken {
		t.Errorf("AccessToken = %q, want %q", tok.AccessToken, respToken)
	}
	if tok.RefreshToken != "refresh-abc" {
		t.Errorf("RefreshToken = %q, want refresh-abc", tok.RefreshToken)
	}
	if tok.ExpiresAt.IsZero() {
		t.Error("ExpiresAt should not be zero")
	}
}

func TestExchangeCode_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	tokenDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", tokenDir)

	block := buildBlock(t, fmt.Sprintf("hmac_secret = \"s\"\napi_base = %q", srv.URL))
	prov, err := ibmbob.New(block, ibmbob.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = prov.ExchangeCode(context.Background(), "code")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected 403 error, got: %v", err)
	}
}

// --- tokenFromResponse fallbacks ---

func TestExchangeCode_ExpiresInFallback(t *testing.T) {
	// No JWT exp, no expires_at — server returns only expires_in.
	plainToken := "not.a.realjwt" // won't parse as JWT exp
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      plainToken,
			"expires_in": 3600.0,
		})
	}))
	defer srv.Close()

	tokenDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", tokenDir)

	block := buildBlock(t, fmt.Sprintf("hmac_secret = \"s\"\napi_base = %q", srv.URL))
	prov, err := ibmbob.New(block, ibmbob.Options{})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := prov.ExchangeCode(context.Background(), "code")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	// expires_in=3600 − 300s buffer → ~55 min from now; just check it's in the future.
	if !tok.ExpiresAt.After(time.Now()) {
		t.Errorf("ExpiresAt %v should be in the future", tok.ExpiresAt)
	}
}

func TestExchangeCode_ExpiresAtFallback(t *testing.T) {
	// expires_at in epoch-ms, no JWT exp.
	plainToken := "not.a.realjwt"
	futureMs := float64(time.Now().Add(2 * time.Hour).UnixMilli())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      plainToken,
			"expires_at": futureMs,
		})
	}))
	defer srv.Close()

	tokenDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", tokenDir)

	block := buildBlock(t, fmt.Sprintf("hmac_secret = \"s\"\napi_base = %q", srv.URL))
	prov, err := ibmbob.New(block, ibmbob.Options{})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := prov.ExchangeCode(context.Background(), "code")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if !tok.ExpiresAt.After(time.Now()) {
		t.Errorf("ExpiresAt %v should be in the future", tok.ExpiresAt)
	}
}

// --- Provider.Name() ---

func TestProvider_Name(t *testing.T) {
	block := buildBlock(t, `api_key = "sk-x"`)
	p, err := ibmbob.New(block, ibmbob.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Name(); got != "bob" {
		t.Errorf("Name() = %q, want bob", got)
	}
}

// --- Login URL resolution ---

func TestLoginURL_DefaultProductionHost(t *testing.T) {
	// api.us-east.bob.ibm.com must resolve to the canonical front-end login page,
	// not to the API host itself.
	block := buildBlock(t, `api_key = "sk-x"`)
	p, err := ibmbob.New(block, ibmbob.Options{})
	if err != nil {
		t.Fatal(err)
	}
	const want = "https://bob.ibm.com/login"
	if got := p.LoginURL(); got != want {
		t.Errorf("LoginURL() = %q, want %q", got, want)
	}
}

func TestLoginURL_DevHost(t *testing.T) {
	block := buildBlock(t, `hmac_secret = "s"
api_base = "https://api.dev.bob.ibm.com"`)
	p, err := ibmbob.New(block, ibmbob.Options{})
	if err != nil {
		t.Fatal(err)
	}
	const want = "https://public-dev.bob.ibm.com/login"
	if got := p.LoginURL(); got != want {
		t.Errorf("LoginURL() = %q, want %q", got, want)
	}
}

func TestLoginURL_QAHost(t *testing.T) {
	block := buildBlock(t, `hmac_secret = "s"
api_base = "https://api.qa-test.bob.ibm.com"`)
	p, err := ibmbob.New(block, ibmbob.Options{})
	if err != nil {
		t.Fatal(err)
	}
	const want = "https://qa.bob.ibm.com/login"
	if got := p.LoginURL(); got != want {
		t.Errorf("LoginURL() = %q, want %q", got, want)
	}
}

func TestLoginURL_UnknownHostFallsBackToDefault(t *testing.T) {
	// An unrecognised host (e.g. a custom deployment) falls back to DefaultLoginURL.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	block := buildBlock(t, fmt.Sprintf(`hmac_secret = "s"
api_base = %q`, srv.URL))
	p, err := ibmbob.New(block, ibmbob.Options{})
	if err != nil {
		t.Fatal(err)
	}
	const want = ibmbob.DefaultLoginURL
	if got := p.LoginURL(); got != want {
		t.Errorf("LoginURL() = %q, want %q", got, want)
	}
}

func TestLoginURL_ExplicitOverride(t *testing.T) {
	// sso_login_base is honoured verbatim.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	block := buildBlock(t, fmt.Sprintf(`hmac_secret = "s"
api_base = %q
sso_login_base = "https://custom.example.com/login"`, srv.URL))
	p, err := ibmbob.New(block, ibmbob.Options{})
	if err != nil {
		t.Fatal(err)
	}
	const want = "https://custom.example.com/login"
	if got := p.LoginURL(); got != want {
		t.Errorf("LoginURL() = %q, want %q", got, want)
	}
}
