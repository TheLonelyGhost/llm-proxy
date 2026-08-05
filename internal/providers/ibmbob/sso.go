package ibmbob

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/thelonelyghost/llm-proxy/internal/paths"
)

// ErrSSOExpired is returned when the stored SSO token has expired and no
// refresh_token is available. The operator must re-run
// `llm-proxy ibm-bob login` to obtain a new credential.
var ErrSSOExpired = errors.New("ibmbob SSO token expired; run `llm-proxy ibm-bob login`")

// SSOToken holds the IBM Bob access + refresh token pair persisted on disk
// and cached in memory. All fields use snake_case JSON keys to match the
// IBM Bob token endpoint response shape.
type SSOToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// Expired reports whether the token should be refreshed. Uses a 5-minute
// pre-emptive buffer matching pi-dangit-bobby's jwtExpiry behaviour.
func (t *SSOToken) Expired(now time.Time) bool {
	return now.Add(5 * time.Minute).After(t.ExpiresAt)
}

// bobTokenResponse is the raw JSON returned by /authn/v1/auth/token and
// /authn/v1/auth/refresh. The field names match the IBM Bob wire format.
type bobTokenResponse struct {
	Token        string  `json:"token"`
	RefreshToken string  `json:"refresh_token,omitempty"`
	ExpiresIn    float64 `json:"expires_in,omitempty"` // seconds
	ExpiresAt    float64 `json:"expires_at,omitempty"` // epoch-milliseconds
}

// tokenFromResponse normalises a bobTokenResponse into an SSOToken. Expiry
// is computed in priority order:
//  1. JWT exp claim − 5 min (matches pi-dangit-bobby:jwtExpiry)
//  2. expires_at (epoch-ms) − 5 min
//  3. now + expires_in seconds (defaulting to 55 min) − 5 min
func tokenFromResponse(resp bobTokenResponse, now time.Time) (*SSOToken, error) {
	if resp.Token == "" {
		return nil, fmt.Errorf("ibmbob SSO: empty token in response")
	}
	const buffer = 5 * time.Minute
	var expiresAt time.Time
	if t, ok := jwtExpiry(resp.Token); ok {
		expiresAt = t // already has 5-min buffer applied
	} else if resp.ExpiresAt != 0 {
		expiresAt = time.UnixMilli(int64(resp.ExpiresAt)).Add(-buffer)
	} else {
		secs := resp.ExpiresIn
		if secs <= 0 {
			secs = 55 * 60
		}
		expiresAt = now.Add(time.Duration(secs)*time.Second - buffer)
	}
	return &SSOToken{
		AccessToken:  resp.Token,
		RefreshToken: resp.RefreshToken,
		ExpiresAt:    expiresAt,
	}, nil
}

// jwtExpiry extracts the exp claim from the JWT payload segment and returns
// the expiry time with a 5-minute pre-emptive buffer applied. Returns
// (zero, false) when the input is not a recognisable JWT or lacks an exp
// claim.
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
	return time.Unix(claims.Exp, 0).Add(-5 * time.Minute), true
}

// ensureSSO ensures a valid SSO access token is loaded into p.ssoCached. It
// is a no-op when a static api_key is configured (p.tokenPath == "").
//
// Lookup order:
//  1. In-memory cache hit → return immediately (no I/O).
//  2. Load from disk; if not expired → populate in-memory cache.
//  3. Disk token expired + RefreshToken present → refresh, write disk, cache.
//  4. No usable token → return ErrSSOExpired.
func (p *Provider) ensureSSO(ctx context.Context) error {
	if p.tokenPath == "" {
		// Static api_key path — nothing to do.
		return nil
	}

	p.ssoMu.Lock()
	defer p.ssoMu.Unlock()

	now := p.now()

	// 1. Fast path: in-memory cache is valid.
	if p.ssoCached != nil && !p.ssoCached.Expired(now) {
		return nil
	}

	// 2. Load from disk.
	disk, err := loadSSOToken(p.tokenPath)
	if err == nil && !disk.Expired(now) {
		p.ssoCached = disk
		return nil
	}

	// 3. Disk token expired but we have a refresh token — try to refresh.
	refreshToken := ""
	if disk != nil {
		refreshToken = disk.RefreshToken
	}
	if p.ssoCached != nil && p.ssoCached.RefreshToken != "" {
		refreshToken = p.ssoCached.RefreshToken
	}
	if refreshToken != "" {
		tok, err := p.doRefresh(ctx, refreshToken)
		if err != nil {
			return fmt.Errorf("ibmbob SSO refresh: %w", err)
		}
		if err := saveSSOToken(p.tokenPath, tok); err != nil {
			// Non-fatal: we have the token in memory; log but continue.
			p.logger.Warn("ibmbob: failed to persist refreshed SSO token",
				"backend", p.name, "err", err)
		}
		p.ssoCached = tok
		return nil
	}

	// 4. No usable credential.
	return ErrSSOExpired
}

// doRefresh calls POST /authn/v1/auth/refresh and returns the new SSOToken.
// It must not hold p.ssoMu (the caller already does).
func (p *Provider) doRefresh(ctx context.Context, refreshToken string) (*SSOToken, error) {
	payload, err := json.Marshal(map[string]string{"refresh_token": refreshToken})
	if err != nil {
		return nil, fmt.Errorf("marshal refresh body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.ssoAuthBase+"/authn/v1/auth/refresh",
		bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", p.settings.UserAgent)
	// No Authorization header — the token endpoint is unauthenticated.

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh request: %w", err)
	}
	if resp == nil {
		return nil, fmt.Errorf("refresh request: nil response")
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read refresh response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("refresh HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var raw bobTokenResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode refresh response: %w", err)
	}
	return tokenFromResponse(raw, p.now())
}

// ExchangeCode POSTs {"code": code} to /authn/v1/auth/token and returns the
// resulting SSOToken. Used only during the login subcommand flow.
func (p *Provider) ExchangeCode(ctx context.Context, code string) (*SSOToken, error) {
	payload, err := json.Marshal(map[string]string{"code": code})
	if err != nil {
		return nil, fmt.Errorf("marshal code body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.ssoAuthBase+"/authn/v1/auth/token",
		bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", p.settings.UserAgent)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	if resp == nil {
		return nil, fmt.Errorf("token request: nil response")
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read token response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("token HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var raw bobTokenResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	return tokenFromResponse(raw, p.now())
}

// Login runs the interactive SSO Authorization Code flow:
//  1. Starts a local callback HTTP server on a random port.
//  2. Calls onURL with the constructed IBM Bob login URL.
//  3. Waits for the browser redirect; validates the state nonce.
//  4. Exchanges the one-time code for a token pair.
//  5. Writes the token file (mode 0600) and hot-loads into memory.
//
// onURL is called exactly once before the function blocks waiting for the
// browser redirect. It should print or open the URL for the operator.
func (p *Provider) Login(ctx context.Context, onURL func(string)) error {
	if p.tokenPath == "" {
		return fmt.Errorf("ibmbob: SSO login is not available when api_key is set in Configuration")
	}

	cs, err := startCallbackServer(ctx)
	if err != nil {
		return fmt.Errorf("start callback server: %w", err)
	}
	defer cs.close()

	loginURL := p.loginURL + "?" + url.Values{
		"callback_uri": {cs.callbackURI},
		"state":        {cs.state},
	}.Encode()
	onURL(loginURL)

	select {
	case code := <-cs.codeCh:
		tok, err := p.ExchangeCode(ctx, code)
		if err != nil {
			return err
		}
		if err := saveSSOToken(p.tokenPath, tok); err != nil {
			return fmt.Errorf("persist token: %w", err)
		}
		p.ssoMu.Lock()
		p.ssoCached = tok
		p.ssoMu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Reload clears the in-memory SSO credential cache so the next request
// re-reads the token file from disk. Safe for concurrent use. Implements
// backend.Reloadable.
func (p *Provider) Reload() {
	p.ssoMu.Lock()
	p.ssoCached = nil
	p.ssoMu.Unlock()
	p.logger.Info("ibmbob: SSO token cache cleared (SIGHUP)", "backend", p.name)
}

// callbackServer holds the state for the local OAuth redirect listener.
type callbackServer struct {
	callbackURI string
	state       string
	codeCh      <-chan string
	close       func()
}

// startCallbackServer binds a random local TCP port, registers a callback
// handler, and returns a callbackServer ready to receive the OAuth redirect.
func startCallbackServer(ctx context.Context) (*callbackServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("bind callback listener: %w", err)
	}

	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("generate state nonce: %w", err)
	}
	state := hex.EncodeToString(stateBytes)

	codeCh := make(chan string, 1)
	callbackURI := "http://" + ln.Addr().String() + "/callback"

	mux := http.NewServeMux()
	srv := &http.Server{Handler: mux} //nolint:gosec // local callback only, no timeout needed
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			http.Error(w, "invalid state", http.StatusBadRequest)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "Authentication successful. You may close this tab.")
		select {
		case codeCh <- code:
		default:
		}
	})

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	closeFunc := func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}

	// Surface any immediate bind/serve failure.
	select {
	case serveErr := <-errCh:
		closeFunc()
		// serveErr may be nil when Serve stops cleanly; treat both as failure
		// since the server should still be running at this point.
		if serveErr != nil {
			return nil, serveErr
		}
		return nil, fmt.Errorf("callback server exited unexpectedly")
	case <-ctx.Done():
		closeFunc()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("context cancelled before callback server started")
	default:
	}

	return &callbackServer{
		callbackURI: callbackURI,
		state:       state,
		codeCh:      codeCh,
		close:       closeFunc,
	}, nil
}

// currentCredential returns the (credential, kind) pair to use for outbound
// Authorization headers. The SSO access token takes priority over the static
// api_key. Callers must not hold p.ssoMu.
func (p *Provider) currentCredential() (string, KeyKind) {
	p.ssoMu.Lock()
	sso := p.ssoCached
	p.ssoMu.Unlock()
	if sso != nil && sso.AccessToken != "" {
		// SSO access tokens are always JWTs → Bearer scheme.
		return sso.AccessToken, KeyKindJWT
	}
	return p.settings.APIKey, p.keyKind
}

// --- on-disk helpers ---

func loadSSOToken(path string) (*SSOToken, error) {
	data, err := os.ReadFile(path) //nolint:gosec // proxy-internal path under cache dir
	if err != nil {
		return nil, err
	}
	var tok SSOToken
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, fmt.Errorf("decode SSO token %q: %w", path, err)
	}
	return &tok, nil
}

func saveSSOToken(path string, tok *SSOToken) error {
	b, err := json.MarshalIndent(tok, "", "  ") //nolint:gosec // SSO token is intentionally persisted to a secrets file
	if err != nil {
		return fmt.Errorf("encode SSO token: %w", err)
	}
	return paths.WriteSecret(path, b)
}
