package ibmbob_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
	"github.com/thelonelyghost/llm-proxy/internal/config"
	"github.com/thelonelyghost/llm-proxy/internal/providers/ibmbob"
)

// buildBlock parses a backend block stanza using the real config loader to
// produce a config.BackendBlock for the test.
func buildBlock(t *testing.T, body string) config.BackendBlock {
	t.Helper()
	full := fmt.Sprintf("backend \"bob\" {\n  type = \"%s\"\n  %s\n}\n", ibmbob.Type, body)
	dir := t.TempDir()
	p := filepath.Join(dir, "c.hcl")
	if err := os.WriteFile(p, []byte(full), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg.Backends[0]
}

func fixedNow() func() time.Time {
	t0 := time.Date(2026, 5, 18, 0, 0, 0, 0, time.UTC)
	return func() time.Time { return t0 }
}

func makeJWTKey() string {
	mid := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString([]byte(`{"sub":"x"}`))
	return "header." + mid + ".sig"
}

func TestNew_RequiresAPIKey(t *testing.T) {
	block := buildBlock(t, ``)
	if _, err := ibmbob.New(block, ibmbob.Options{}); err == nil || !strings.Contains(err.Error(), "api_key is required") {
		t.Fatalf("err = %v", err)
	}
}

func TestNew_WrongType(t *testing.T) {
	if _, err := ibmbob.New(config.BackendBlock{Name: "x", Type: "other"}, ibmbob.Options{}); err == nil {
		t.Fatal("expected wrong-type error")
	}
}

func TestNew_RequiresHMACForAuthnBackend(t *testing.T) {
	block := buildBlock(t, fmt.Sprintf(`api_key = "%s"`, makeJWTKey()))
	if _, err := ibmbob.New(block, ibmbob.Options{}); err == nil || !strings.Contains(err.Error(), "hmac_secret") {
		t.Fatalf("err = %v", err)
	}
}

func TestNew_LegacyKeyDoesNotRequireHMAC(t *testing.T) {
	block := buildBlock(t, `api_key = "sk-test"`)
	p, err := ibmbob.New(block, ibmbob.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p.KeyKind() != ibmbob.KeyKindLegacy {
		t.Errorf("kind = %v", p.KeyKind())
	}
	if p.BaseURL() != ibmbob.DefaultOldBackendURL {
		t.Errorf("base = %q", p.BaseURL())
	}
}

func TestNew_BadTimeout(t *testing.T) {
	block := buildBlock(t, `api_key = "sk-x"
  request_timeout = "garbage"`)
	if _, err := ibmbob.New(block, ibmbob.Options{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestNew_BadRetryDelay(t *testing.T) {
	block := buildBlock(t, `api_key = "sk-x"
  retry_delay = "-1s"`)
	if _, err := ibmbob.New(block, ibmbob.Options{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestProvider_AllowList(t *testing.T) {
	block := buildBlock(t, `api_key = "sk-x"
  models = ["premium"]`)
	p, err := ibmbob.New(block, ibmbob.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Chat(context.Background(), backend.ChatRequest{Model: "free"})
	if err == nil || !strings.Contains(err.Error(), "allow-list") {
		t.Fatalf("err = %v", err)
	}
}

// fakeBackend stands in for the IBM Bob upstream. It validates signing
// headers when configured to.
type fakeBackend struct {
	t              *testing.T
	expectSign     bool
	expectInstance string
	expectTeam     string
	hmacSecret     []byte

	chatCalls       int32
	profileCalls    int32
	embedCalls      int32
	completionCalls int32

	chatResp       string
	embedResp      string
	completionResp string
	profileResp    string

	chatStatus    int
	failChatTimes int
}

func TestForward_HappyPath_AuthnBackend(t *testing.T) {
	fb := &fakeBackend{t: t, expectSign: true, expectInstance: "inst-1", expectTeam: "team-1"}
	fb.hmacSecret = []byte("topsecret")
	fb.chatResp = `{"id":"x","object":"chat.completion","model":"premium","choices":[]}`
	fb.profileResp = `{"instances":[{"instance_id":"inst-1","teams":[{"id":"team-1"}]}]}`
	srv := startFake(t, fb)

	block := buildBlock(t, fmt.Sprintf(`
        api_key      = "%s"
        hmac_secret  = "topsecret"
        api_base     = "%s"
        models       = ["premium"]
    `, makeJWTKey(), srv.URL))

	p, err := ibmbob.New(block, ibmbob.Options{Now: fixedNow()})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Chat(context.Background(), backend.ChatRequest{
		Model: "premium",
		Body:  []byte(`{"model":"premium","messages":[]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if atomic.LoadInt32(&fb.profileCalls) != 1 {
		t.Errorf("expected 1 profile call, got %d", fb.profileCalls)
	}
	if atomic.LoadInt32(&fb.chatCalls) != 1 {
		t.Errorf("expected 1 chat call, got %d", fb.chatCalls)
	}

	// Second call reuses cached identity - no further profile lookup.
	resp2, err := p.Chat(context.Background(), backend.ChatRequest{
		Model: "premium",
		Body:  []byte(`{"model":"premium","messages":[]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if atomic.LoadInt32(&fb.profileCalls) != 1 {
		t.Errorf("expected still 1 profile call, got %d", fb.profileCalls)
	}
}

func TestForward_LegacyKey_SkipsProfileAndSigning(t *testing.T) {
	fb := &fakeBackend{t: t, expectSign: false}
	fb.chatResp = `{"id":"x","model":"premium"}`
	srv := startFake(t, fb)

	block := buildBlock(t, fmt.Sprintf(`
        api_key  = "sk-test"
        api_base = "%s"
        models   = ["premium"]
    `, srv.URL))
	p, err := ibmbob.New(block, ibmbob.Options{Now: fixedNow()})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Chat(context.Background(), backend.ChatRequest{
		Model: "premium",
		Body:  []byte(`{"model":"premium"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if atomic.LoadInt32(&fb.profileCalls) != 0 {
		t.Errorf("legacy key should not fetch profile, got %d calls", fb.profileCalls)
	}
}

func TestForward_RetryOn5xx(t *testing.T) {
	fb := &fakeBackend{t: t}
	fb.chatResp = `{"ok":true}`
	fb.failChatTimes = 2
	srv := startFake(t, fb)

	block := buildBlock(t, fmt.Sprintf(`
        api_key     = "sk-test"
        api_base    = "%s"
        retry_delay = "1ms"
        max_retries = 4
        models      = ["premium"]
    `, srv.URL))
	p, err := ibmbob.New(block, ibmbob.Options{Now: fixedNow()})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Chat(context.Background(), backend.ChatRequest{
		Model: "premium",
		Body:  []byte(`{"model":"premium"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&fb.chatCalls); got != 3 {
		t.Errorf("chatCalls = %d (want 3 = 2 fails + 1 success)", got)
	}
}

func TestForward_RetryOnTransientBodyMarker(t *testing.T) {
	fb := &fakeBackend{t: t}
	fb.chatStatus = 400
	fb.chatResp = `{"error":"Error getting user_id from apikey"}`
	fb.failChatTimes = 0 // every call returns 400+marker
	srv := startFake(t, fb)

	block := buildBlock(t, fmt.Sprintf(`
        api_key     = "sk-test"
        api_base    = "%s"
        retry_delay = "1ms"
        max_retries = 3
        models      = ["premium"]
    `, srv.URL))
	p, err := ibmbob.New(block, ibmbob.Options{Now: fixedNow()})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Chat(context.Background(), backend.ChatRequest{
		Model: "premium",
		Body:  []byte(`{"model":"premium"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := atomic.LoadInt32(&fb.chatCalls); got != 3 {
		t.Errorf("chatCalls = %d (want max_retries=3)", got)
	}
}

func TestForward_NoRetryOn4xxWithoutMarker(t *testing.T) {
	fb := &fakeBackend{t: t}
	fb.chatStatus = 400
	fb.chatResp = `{"error":"bad request"}`
	srv := startFake(t, fb)

	block := buildBlock(t, fmt.Sprintf(`
        api_key     = "sk-test"
        api_base    = "%s"
        retry_delay = "1ms"
        models      = ["premium"]
    `, srv.URL))
	p, err := ibmbob.New(block, ibmbob.Options{Now: fixedNow()})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Chat(context.Background(), backend.ChatRequest{
		Model: "premium",
		Body:  []byte(`{"model":"premium"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := atomic.LoadInt32(&fb.chatCalls); got != 1 {
		t.Errorf("chatCalls = %d (want 1)", got)
	}
}

func TestProfile_PicksRequestedInstance(t *testing.T) {
	fb := &fakeBackend{t: t, expectInstance: "inst-2", expectTeam: "team-2"}
	fb.hmacSecret = []byte("s")
	fb.profileResp = `{"instances":[
        {"instance_id":"inst-1","teams":[{"id":"team-1"}]},
        {"instance_id":"inst-2","teams":[{"id":"team-2"},{"id":"team-3"}]}
    ]}`
	fb.chatResp = `{"ok":true}`
	srv := startFake(t, fb)

	block := buildBlock(t, fmt.Sprintf(`
        api_key     = "%s"
        hmac_secret = "s"
        api_base    = "%s"
        instance_id = "inst-2"
        team_id     = "team-2"
        models      = ["premium"]
    `, makeJWTKey(), srv.URL))
	p, err := ibmbob.New(block, ibmbob.Options{Now: fixedNow()})
	if err != nil {
		t.Fatal(err)
	}
	// When both overrides are set, the profile call is skipped.
	resp, err := p.Chat(context.Background(), backend.ChatRequest{
		Model: "premium",
		Body:  []byte(`{"model":"premium"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := atomic.LoadInt32(&fb.profileCalls); got != 0 {
		t.Errorf("expected no profile call when both ids pinned, got %d", got)
	}
}

func TestProfile_FailurePropagates(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/v1/profile", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	block := buildBlock(t, fmt.Sprintf(`
        api_key     = "%s"
        hmac_secret = "s"
        api_base    = "%s"
        models      = ["premium"]
    `, makeJWTKey(), srv.URL))
	p, err := ibmbob.New(block, ibmbob.Options{Now: fixedNow()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Chat(context.Background(), backend.ChatRequest{
		Model: "premium",
		Body:  []byte(`{"model":"premium"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "profile fetch failed") {
		t.Fatalf("err = %v", err)
	}
}

func TestEmbedAndCompletions(t *testing.T) {
	fb := &fakeBackend{t: t}
	fb.embedResp = `{"data":[],"model":"e"}`
	fb.completionResp = `{"id":"c"}`
	srv := startFake(t, fb)

	block := buildBlock(t, fmt.Sprintf(`
        api_key  = "sk-test"
        api_base = "%s"
        models   = ["premium","emb"]
    `, srv.URL))
	p, err := ibmbob.New(block, ibmbob.Options{Now: fixedNow()})
	if err != nil {
		t.Fatal(err)
	}

	r1, err := p.Embed(context.Background(), backend.EmbeddingRequest{
		Model: "emb",
		Body:  []byte(`{"model":"emb","input":"hi"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	r1.Body.Close()
	if atomic.LoadInt32(&fb.embedCalls) != 1 {
		t.Errorf("embed calls = %d", fb.embedCalls)
	}

	r2, err := p.Complete(context.Background(), backend.CompletionRequest{
		Model: "premium",
		Body:  []byte(`{"model":"premium","prompt":"hi"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if atomic.LoadInt32(&fb.completionCalls) != 1 {
		t.Errorf("completion calls = %d", fb.completionCalls)
	}
}

func TestComplete_UnsupportedOnAuthnBackend(t *testing.T) {
	fb := &fakeBackend{t: t}
	fb.hmacSecret = []byte("s")
	fb.profileResp = `{"instances":[{"instance_id":"i","teams":[{"id":"t"}]}]}`
	srv := startFake(t, fb)

	block := buildBlock(t, fmt.Sprintf(`
        api_key     = "%s"
        hmac_secret = "s"
        api_base    = "%s"
        models      = ["premium"]
    `, makeJWTKey(), srv.URL))
	p, err := ibmbob.New(block, ibmbob.Options{Now: fixedNow()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Complete(context.Background(), backend.CompletionRequest{
		Model: "premium",
		Body:  []byte(`{"model":"premium"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "does not expose legacy") {
		t.Fatalf("err = %v", err)
	}
}

func TestModels(t *testing.T) {
	block := buildBlock(t, `api_key = "sk-x"
  models = ["a","b"]`)
	p, err := ibmbob.New(block, ibmbob.Options{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].ID != "a" || out[1].ID != "b" {
		t.Errorf("models = %+v", out)
	}
}

// --- fake backend helpers ---

func startFake(t *testing.T, fb *fakeBackend) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	registerHandler := func(path string, h http.HandlerFunc) { mux.HandleFunc(path, h) }
	registerHandler("/admin/v1/profile", fb.handleProfile)
	registerHandler("/inference/v1/chat/completions", fb.handleChat)
	registerHandler("/chat/completions", fb.handleChat)
	registerHandler("/embeddings", fb.handleEmbed)
	registerHandler("/v1/completions", fb.handleCompletion)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeBackend) verifyHeaders(r *http.Request) {
	f.t.Helper()
	if r.Header.Get("Authorization") == "" {
		f.t.Errorf("missing Authorization header")
	}
	if f.expectSign {
		if r.Header.Get("x-request-timestamp") == "" || r.Header.Get("x-request-signature") == "" {
			f.t.Errorf("missing signature headers; got %v", r.Header)
		}
	}
	if f.expectInstance != "" && r.Header.Get("x-instance-id") != f.expectInstance {
		f.t.Errorf("x-instance-id = %q, want %q", r.Header.Get("x-instance-id"), f.expectInstance)
	}
	if f.expectTeam != "" && r.Header.Get("x-team-id") != f.expectTeam {
		f.t.Errorf("x-team-id = %q, want %q", r.Header.Get("x-team-id"), f.expectTeam)
	}
}

func (f *fakeBackend) handleProfile(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt32(&f.profileCalls, 1)
	if f.profileResp == "" {
		http.Error(w, "no profile", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, f.profileResp) //nolint:errcheck
}

func (f *fakeBackend) handleChat(w http.ResponseWriter, r *http.Request) {
	f.verifyHeaders(r)
	count := atomic.AddInt32(&f.chatCalls, 1)
	if f.failChatTimes > 0 && int32(f.failChatTimes) >= count {
		http.Error(w, "boom", http.StatusBadGateway)
		return
	}
	status := f.chatStatus
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	io.WriteString(w, f.chatResp) //nolint:errcheck
}

func (f *fakeBackend) handleEmbed(w http.ResponseWriter, r *http.Request) {
	f.verifyHeaders(r)
	atomic.AddInt32(&f.embedCalls, 1)
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, f.embedResp) //nolint:errcheck
}

func (f *fakeBackend) handleCompletion(w http.ResponseWriter, r *http.Request) {
	f.verifyHeaders(r)
	atomic.AddInt32(&f.completionCalls, 1)
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, f.completionResp) //nolint:errcheck
}

// Avoid unused warnings during refactors.
var (
	_ = json.Marshal
)
