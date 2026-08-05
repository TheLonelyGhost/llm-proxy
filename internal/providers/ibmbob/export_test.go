// export_test.go exposes internal symbols for use by tests in the ibmbob_test
// package. It is compiled only during testing.
package ibmbob

import (
	"context"
	"time"
)

// LoginURL returns the resolved browser-facing login URL. Used in tests.
func (p *Provider) LoginURL() string { return p.loginURL }

// JWTExpiry exports jwtExpiry for black-box testing.
func JWTExpiry(token string) (time.Time, bool) {
	return jwtExpiry(token)
}

// EnsureSSO exports ensureSSO for black-box testing.
func (p *Provider) EnsureSSO(ctx context.Context) error {
	return p.ensureSSO(ctx)
}

// CurrentAccessToken returns the access token from the in-memory SSO cache,
// or "" if none is cached. Used in tests only.
func (p *Provider) CurrentAccessToken() string {
	p.ssoMu.Lock()
	defer p.ssoMu.Unlock()
	if p.ssoCached != nil {
		return p.ssoCached.AccessToken
	}
	return ""
}

// SetCachedSSO directly sets the in-memory SSO token cache. Used in tests.
func (p *Provider) SetCachedSSO(tok *SSOToken) {
	p.ssoMu.Lock()
	defer p.ssoMu.Unlock()
	p.ssoCached = tok
}

// TestCallbackServer is an exported view of callbackServer for tests.
type TestCallbackServer struct {
	CallbackURI string
	State       string
	CodeCh      <-chan string
	cs          *callbackServer
}

// Close shuts down the callback server.
func (tcs *TestCallbackServer) Close() { tcs.cs.close() }

// StartTestCallbackServer starts a callback server and returns a
// TestCallbackServer that exposes its internal fields for test assertions.
func StartTestCallbackServer(ctx context.Context) (*TestCallbackServer, error) {
	cs, err := startCallbackServer(ctx)
	if err != nil {
		return nil, err
	}
	return &TestCallbackServer{
		CallbackURI: cs.callbackURI,
		State:       cs.state,
		CodeCh:      cs.codeCh,
		cs:          cs,
	}, nil
}
