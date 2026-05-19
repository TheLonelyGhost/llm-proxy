package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/thelonelyghost/llm-proxy/internal/paths"
)

// GitHubToken is the long-lived OAuth token obtained via device-code flow.
type GitHubToken struct {
	AccessToken string    `json:"access_token"`
	TokenType   string    `json:"token_type"`
	Scope       string    `json:"scope"`
	ObtainedAt  time.Time `json:"obtained_at"`
}

// CopilotToken is the short-lived API token exchanged from a GitHubToken.
type CopilotToken struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	// Endpoints carries upstream-specified API endpoints, when present.
	Endpoints map[string]string `json:"endpoints,omitempty"`
}

// Expiry returns the absolute expiry time.
func (t CopilotToken) Expiry() time.Time { return time.Unix(t.ExpiresAt, 0) }

// Expired reports whether the token is within skew of its expiry.
func (t CopilotToken) Expired(now time.Time, skew time.Duration) bool {
	return now.Add(skew).After(t.Expiry())
}

// TokenStore persists credentials between proxy invocations.
type TokenStore interface {
	LoadGitHubToken() (*GitHubToken, error)
	SaveGitHubToken(*GitHubToken) error
	LoadCopilotToken() (*CopilotToken, error)
	SaveCopilotToken(*CopilotToken) error
	// DeleteCopilotToken clears the cached short-lived token. A missing
	// file is not an error.
	DeleteCopilotToken() error
	// DeleteAll removes any cached credentials (e.g. on logout).
	DeleteAll() error
}

// ErrNotAuthenticated indicates no GitHub token is available; the user must
// run `llm-proxy login` first.
var ErrNotAuthenticated = errors.New("not authenticated: run `llm-proxy login`")

// FileTokenStore persists tokens under platform-appropriate dirs.
type FileTokenStore struct {
	configDir string
	cacheDir  string
}

// NewFileTokenStore creates a FileTokenStore rooted at the platform-default
// directories.
func NewFileTokenStore() (*FileTokenStore, error) {
	cfg, err := paths.ConfigDir()
	if err != nil {
		return nil, err
	}
	cache, err := paths.CacheDir()
	if err != nil {
		return nil, err
	}
	return &FileTokenStore{configDir: cfg, cacheDir: cache}, nil
}

// NewFileTokenStoreAt creates a FileTokenStore at explicit paths (for tests).
func NewFileTokenStoreAt(configDir, cacheDir string) *FileTokenStore {
	return &FileTokenStore{configDir: configDir, cacheDir: cacheDir}
}

func (s *FileTokenStore) ghPath() string { return filepath.Join(s.configDir, "github_token.json") }
func (s *FileTokenStore) cpPath() string { return filepath.Join(s.cacheDir, "copilot_token.json") }

// LoadGitHubToken reads the persisted GitHub token, returning
// ErrNotAuthenticated if none is present.
func (s *FileTokenStore) LoadGitHubToken() (*GitHubToken, error) {
	return loadJSON[GitHubToken](s.ghPath(), ErrNotAuthenticated)
}

// SaveGitHubToken writes the GitHub token atomically with mode 0600.
func (s *FileTokenStore) SaveGitHubToken(t *GitHubToken) error {
	return saveJSON(s.ghPath(), t)
}

// LoadCopilotToken reads a cached short-lived Copilot token. A missing file
// returns (nil, nil); the caller will refresh.
func (s *FileTokenStore) LoadCopilotToken() (*CopilotToken, error) {
	t, err := loadJSON[CopilotToken](s.cpPath(), nil)
	if errors.Is(err, errFileMissing) {
		return nil, nil
	}
	return t, err
}

// SaveCopilotToken writes the cached Copilot token atomically.
func (s *FileTokenStore) SaveCopilotToken(t *CopilotToken) error {
	return saveJSON(s.cpPath(), t)
}

// DeleteCopilotToken removes the cached short-lived token if present.
func (s *FileTokenStore) DeleteCopilotToken() error {
	return removeIfExists(s.cpPath())
}

// DeleteAll removes both tokens.
func (s *FileTokenStore) DeleteAll() error {
	var firstErr error
	for _, p := range []string{s.ghPath(), s.cpPath()} {
		if err := removeIfExists(p); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

var errFileMissing = errors.New("token file missing")

func loadJSON[T any](path string, missingErr error) (*T, error) {
	data, err := readFile(path)
	if err != nil {
		if errors.Is(err, errFileMissing) {
			if missingErr != nil {
				return nil, missingErr
			}
			return nil, errFileMissing
		}
		return nil, err
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("decode %q: %w", path, err)
	}
	return &v, nil
}

func saveJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode token: %w", err)
	}
	return paths.WriteSecret(path, b)
}

// AuthConfig parameterises an Authenticator.
type AuthConfig struct {
	ClientID        string
	GitHubAPIBase   string
	GitHubLoginBase string
	UserAgent       string
	EditorVersion   string
	Store           TokenStore
	HTTPClient      *http.Client
	// Skew controls how early to refresh a Copilot token before its
	// expiry. Zero uses DefaultRefreshSkew.
	Skew time.Duration
	// IntervalUnit is the duration represented by a "1" in
	// device-code interval responses. Zero means time.Second. Tests can
	// override this to make polling subsecond.
	IntervalUnit time.Duration
}

// DefaultRefreshSkew is the buffer subtracted from a Copilot token's expiry
// when deciding whether to refresh.
const DefaultRefreshSkew = 5 * time.Minute

// Authenticator manages the GitHub OAuth + Copilot token exchange.
type Authenticator struct {
	cfg AuthConfig

	mu       sync.Mutex
	cpCached *CopilotToken
}

// NewAuthenticator constructs an Authenticator.
func NewAuthenticator(cfg AuthConfig) *Authenticator {
	if cfg.Skew == 0 {
		cfg.Skew = DefaultRefreshSkew
	}
	if cfg.IntervalUnit == 0 {
		cfg.IntervalUnit = time.Second
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Authenticator{cfg: cfg}
}

// DeviceCodeResponse mirrors GitHub's /login/device/code response.
type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// RequestDeviceCode initiates the device-code flow.
func (a *Authenticator) RequestDeviceCode(ctx context.Context) (*DeviceCodeResponse, error) {
	form := url.Values{}
	form.Set("client_id", a.cfg.ClientID)
	form.Set("scope", "read:user")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.cfg.GitHubLoginBase+"/login/device/code", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", a.cfg.UserAgent)

	body, err := a.do(req)
	if err != nil {
		return nil, fmt.Errorf("request device code: %w", err)
	}
	var resp DeviceCodeResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode device-code response: %w", err)
	}
	if resp.DeviceCode == "" || resp.UserCode == "" {
		return nil, fmt.Errorf("incomplete device-code response: %s", string(body))
	}
	if resp.Interval <= 0 {
		resp.Interval = 5
	}
	return &resp, nil
}

// PollForToken polls GitHub until the user has authorised, the request
// expires, or ctx is cancelled.
func (a *Authenticator) PollForToken(ctx context.Context, code *DeviceCodeResponse) (*GitHubToken, error) {
	interval := time.Duration(code.Interval) * a.cfg.IntervalUnit
	deadline := time.Now().Add(time.Duration(code.ExpiresIn) * a.cfg.IntervalUnit)
	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("device code expired before authorisation")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}

		form := url.Values{}
		form.Set("client_id", a.cfg.ClientID)
		form.Set("device_code", code.DeviceCode)
		form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			a.cfg.GitHubLoginBase+"/login/oauth/access_token", strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("User-Agent", a.cfg.UserAgent)

		body, err := a.do(req)
		if err != nil {
			return nil, fmt.Errorf("poll for token: %w", err)
		}

		var payload struct {
			AccessToken      string `json:"access_token"`
			TokenType        string `json:"token_type"`
			Scope            string `json:"scope"`
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
			Interval         int    `json:"interval"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("decode poll response: %w", err)
		}
		if payload.AccessToken != "" {
			tok := &GitHubToken{
				AccessToken: payload.AccessToken,
				TokenType:   payload.TokenType,
				Scope:       payload.Scope,
				ObtainedAt:  time.Now().UTC(),
			}
			if err := a.cfg.Store.SaveGitHubToken(tok); err != nil {
				return nil, fmt.Errorf("save github token: %w", err)
			}
			return tok, nil
		}
		switch payload.Error {
		case "authorization_pending":
			// keep polling
		case "slow_down":
			if payload.Interval > 0 {
				interval = time.Duration(payload.Interval) * a.cfg.IntervalUnit
			} else {
				interval += 5 * a.cfg.IntervalUnit
			}
		case "expired_token", "access_denied":
			return nil, fmt.Errorf("device-code flow failed: %s: %s", payload.Error, payload.ErrorDescription)
		case "":
			return nil, fmt.Errorf("empty poll response: %s", string(body))
		default:
			return nil, fmt.Errorf("device-code flow error: %s: %s", payload.Error, payload.ErrorDescription)
		}
	}
}

// Logout removes any stored credentials.
func (a *Authenticator) Logout() error {
	a.mu.Lock()
	a.cpCached = nil
	a.mu.Unlock()
	return a.cfg.Store.DeleteAll()
}

// CopilotToken returns a valid short-lived Copilot API token, refreshing
// from disk cache or via exchange as needed.
func (a *Authenticator) CopilotToken(ctx context.Context) (*CopilotToken, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := time.Now()

	if a.cpCached != nil && !a.cpCached.Expired(now, a.cfg.Skew) {
		return a.cpCached, nil
	}
	if a.cpCached == nil {
		if t, err := a.cfg.Store.LoadCopilotToken(); err == nil && t != nil {
			a.cpCached = t
			if !t.Expired(now, a.cfg.Skew) {
				return t, nil
			}
		}
	}
	gh, err := a.cfg.Store.LoadGitHubToken()
	if err != nil {
		return nil, err
	}
	tok, err := a.exchange(ctx, gh)
	if err != nil {
		return nil, err
	}
	if err := a.cfg.Store.SaveCopilotToken(tok); err != nil {
		return nil, fmt.Errorf("save copilot token: %w", err)
	}
	a.cpCached = tok
	return tok, nil
}

// Invalidate drops any cached Copilot token (both in-memory and on disk) so
// the next CopilotToken call refreshes from upstream. Used after a 401 from
// the API.
func (a *Authenticator) Invalidate() {
	a.mu.Lock()
	a.cpCached = nil
	a.mu.Unlock()
	_ = a.cfg.Store.DeleteCopilotToken()
}

// exchange calls the GitHub /copilot_internal/v2/token endpoint.
func (a *Authenticator) exchange(ctx context.Context, gh *GitHubToken) (*CopilotToken, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		a.cfg.GitHubAPIBase+"/copilot_internal/v2/token", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+gh.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", a.cfg.UserAgent)
	req.Header.Set("Editor-Version", a.cfg.EditorVersion)
	body, err := a.do(req)
	if err != nil {
		return nil, fmt.Errorf("exchange copilot token: %w", err)
	}
	var tok CopilotToken
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("decode copilot token: %w", err)
	}
	if tok.Token == "" {
		return nil, fmt.Errorf("empty copilot token in response: %s", string(body))
	}
	return &tok, nil
}

func (a *Authenticator) do(req *http.Request) ([]byte, error) {
	resp, err := a.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("nil response from HTTP client")
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("http %d from %s: %s", resp.StatusCode, req.URL, truncate(string(body), 200))
	}
	return body, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Helpers shared with the file store.

func readFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path) //nolint:gosec // proxy-internal paths
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errFileMissing
		}
		return nil, err
	}
	return data, nil
}

// removeIfExists removes path if present, ignoring not-exist errors.
func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
