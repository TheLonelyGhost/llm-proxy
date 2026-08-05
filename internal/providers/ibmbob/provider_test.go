package ibmbob_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	// Without api_key and without hmac_secret the provider must fail because
	// SSO mode still requires hmac_secret for the modern backend.
	block := buildBlock(t, ``)
	if _, err := ibmbob.New(block, ibmbob.Options{}); err == nil || !strings.Contains(err.Error(), "hmac_secret") {
		t.Fatalf("expected hmac_secret error for empty config, got: %v", err)
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

	// chatBody and chatPath capture the most recent /chat/completions call,
	// so tests can verify request translation.
	chatPath string
	chatBody []byte

	// chatHandlerOverride lets a test substitute its own handler (e.g. to
	// emit SSE chunks for streaming tests).
	chatHandlerOverride http.HandlerFunc

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

func TestResponses_AuthnBackend_NonStreaming(t *testing.T) {
	fb := &fakeBackend{t: t, expectSign: true, expectInstance: "inst-1", expectTeam: "team-1"}
	fb.hmacSecret = []byte("topsecret")
	fb.chatResp = `{
		"id":"chatcmpl-bob-1","object":"chat.completion","created":1700000000,
		"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}
	}`
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

	resp, err := p.Responses(context.Background(), backend.ResponsesRequest{
		Model: "premium",
		Body:  []byte(`{"model":"premium","input":"hi","max_output_tokens":32}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if fb.chatPath != "/inference/v1/chat/completions" {
		t.Errorf("upstream path = %q, want /inference/v1/chat/completions", fb.chatPath)
	}

	// Upstream got a chat-completions body, not a responses body.
	var upstream map[string]any
	if err := json.Unmarshal(fb.chatBody, &upstream); err != nil {
		t.Fatal(err)
	}
	if _, isResponses := upstream["input"]; isResponses {
		t.Errorf("upstream received an `input` field; shim did not translate body: %s", fb.chatBody)
	}
	if upstream["max_tokens"] != float64(32) {
		t.Errorf("max_tokens not forwarded: %+v", upstream)
	}
	msgs := upstream["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["content"] != "hi" {
		t.Errorf("messages bad: %+v", msgs)
	}

	// Client receives Responses-shaped JSON.
	body, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("client body: %v", err)
	}
	if out["object"] != "response" || out["status"] != "completed" {
		t.Errorf("response envelope: %+v", out)
	}
	if out["model"] != "bob/premium" {
		t.Errorf("model not prefixed: %v", out["model"])
	}
	output := out["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("output len = %d", len(output))
	}
}

func TestResponses_LegacyBackend_NonStreaming(t *testing.T) {
	fb := &fakeBackend{t: t, expectSign: false}
	fb.chatResp = `{"id":"chatcmpl-1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`
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

	resp, err := p.Responses(context.Background(), backend.ResponsesRequest{
		Model: "premium",
		Body:  []byte(`{"model":"premium","input":"hi"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if fb.chatPath != "/chat/completions" {
		t.Errorf("path = %q, want /chat/completions", fb.chatPath)
	}
	if atomic.LoadInt32(&fb.profileCalls) != 0 {
		t.Errorf("legacy key should not fetch profile, got %d", fb.profileCalls)
	}
}

func TestResponses_AuthnBackend_Streaming(t *testing.T) {
	fb := &fakeBackend{t: t, expectSign: true, expectInstance: "inst-1", expectTeam: "team-1"}
	fb.hmacSecret = []byte("topsecret")
	fb.profileResp = `{"instances":[{"instance_id":"inst-1","teams":[{"id":"team-1"}]}]}`
	fb.chatHandlerOverride = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		write := func(payload string) {
			io.WriteString(w, "data: "+payload+"\n\n") //nolint:errcheck
			if f != nil {
				f.Flush()
			}
		}
		write(`{"id":"chatcmpl-s","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`)
		write(`{"id":"chatcmpl-s","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}`)
		write(`{"id":"chatcmpl-s","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		write(`{"id":"chatcmpl-s","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		write(`[DONE]`)
	}
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

	resp, err := p.Responses(context.Background(), backend.ResponsesRequest{
		Model:  "premium",
		Stream: true,
		Body:   []byte(`{"model":"premium","input":"hi","stream":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q", ct)
	}
	if fb.chatPath != "/inference/v1/chat/completions" {
		t.Errorf("upstream path = %q", fb.chatPath)
	}

	body, _ := io.ReadAll(resp.Body)
	got := string(body)
	wantOrder := []string{
		"event: response.created",
		"event: response.in_progress",
		"event: response.output_item.added",
		"event: response.content_part.added",
		"event: response.output_text.delta",
		"event: response.output_text.done",
		"event: response.content_part.done",
		"event: response.output_item.done",
		"event: response.completed",
	}
	cursor := 0
	for _, want := range wantOrder {
		idx := strings.Index(got[cursor:], want)
		if idx < 0 {
			t.Fatalf("missing or out-of-order event %q. Stream:\n%s", want, got)
		}
		cursor += idx + len(want)
	}
	if !strings.Contains(got, `"input_tokens":1`) || !strings.Contains(got, `"output_tokens":1`) {
		t.Errorf("usage not in completed event:\n%s", got)
	}
}

func TestResponses_AllowListEnforced(t *testing.T) {
	block := buildBlock(t, `api_key = "sk-x"
  models = ["premium"]`)
	p, err := ibmbob.New(block, ibmbob.Options{})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Responses(context.Background(), backend.ResponsesRequest{Model: "other"})
	if resp != nil {
		t.Errorf("expected nil response, got %+v", resp)
	}
	if !errors.Is(err, backend.ErrUnknownModel) {
		t.Fatalf("expected ErrUnknownModel, got %v", err)
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
	f.chatPath = r.URL.Path
	if r.Body != nil {
		body, _ := io.ReadAll(r.Body)
		f.chatBody = body
	}
	if f.chatHandlerOverride != nil {
		f.chatHandlerOverride(w, r)
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

// --- SSO tests ---

func TestNew_SSO_RequiresHMACSecret(t *testing.T) {
	// SSO mode (no api_key) still requires hmac_secret for the modern backend.
	tokenDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", tokenDir)
	block := buildBlock(t, `api_base = "http://127.0.0.1:1"`)
	_, err := ibmbob.New(block, ibmbob.Options{})
	if err == nil || !strings.Contains(err.Error(), "hmac_secret") {
		t.Fatalf("expected hmac_secret error, got: %v", err)
	}
}

func TestNew_SSO_LoadsTokenFromDisk(t *testing.T) {
	tokenDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", tokenDir)

	valid := &ibmbob.SSOToken{
		AccessToken: "disk-sso-token",
		ExpiresAt:   time.Now().Add(1 * time.Hour),
	}
	tokenPath := filepath.Join(tokenDir, "ibmbob_bob_token.json")
	b, _ := json.Marshal(valid)
	if err := os.WriteFile(tokenPath, b, 0o600); err != nil {
		t.Fatal(err)
	}

	block := buildBlock(t, `hmac_secret = "s"`)
	p, err := ibmbob.New(block, ibmbob.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.CurrentAccessToken(); got != "disk-sso-token" {
		t.Errorf("CurrentAccessToken = %q, want disk-sso-token", got)
	}
}

func TestNew_SSO_MissingTokenFile_OK(t *testing.T) {
	// Missing token file is not fatal at New() time.
	tokenDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", tokenDir)
	block := buildBlock(t, `hmac_secret = "s"`)
	_, err := ibmbob.New(block, ibmbob.Options{})
	if err != nil {
		t.Fatalf("New should succeed with missing token file: %v", err)
	}
}

func TestForward_SSOTokenUsed(t *testing.T) {
	// When an in-memory SSO token is cached, it should be used in the
	// Authorization header instead of the (absent) static api_key.
	tokenDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", tokenDir)

	var capturedAuth string
	fb := &fakeBackend{t: t, expectSign: true, expectInstance: "inst-1", expectTeam: "team-1"}
	fb.hmacSecret = []byte("topsecret")
	fb.profileResp = `{"instances":[{"instance_id":"inst-1","teams":[{"id":"team-1"}]}]}`
	fb.chatResp = `{"id":"x"}`
	fb.chatHandlerOverride = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		// Still validate signing.
		fb.verifyHeaders(r)
		atomic.AddInt32(&fb.chatCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, fb.chatResp) //nolint:errcheck
	})
	srv := startFake(t, fb)

	block := buildBlock(t, fmt.Sprintf(
		"hmac_secret = \"topsecret\"\napi_base = %q\nmodels = [\"premium\"]",
		srv.URL,
	))
	p, err := ibmbob.New(block, ibmbob.Options{Now: fixedNow()})
	if err != nil {
		t.Fatal(err)
	}

	// Seed in-memory SSO token.
	ssoTok := &ibmbob.SSOToken{
		AccessToken: makeJWTKey(), // a JWT-shaped token
		ExpiresAt:   time.Now().Add(1 * time.Hour),
	}
	p.SetCachedSSO(ssoTok)

	resp, err := p.Chat(context.Background(), backend.ChatRequest{
		Model: "premium",
		Body:  []byte(`{"model":"premium","messages":[]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if !strings.HasPrefix(capturedAuth, "Bearer ") {
		t.Errorf("Authorization = %q; SSO token should use Bearer scheme", capturedAuth)
	}
	if !strings.Contains(capturedAuth, ssoTok.AccessToken) {
		t.Errorf("Authorization %q does not contain SSO access token", capturedAuth)
	}
}

func TestReloadSSO_DropsCache(t *testing.T) {
	tokenDir := t.TempDir()
	t.Setenv("LLM_PROXY_CACHE_DIR", tokenDir)

	block := buildBlock(t, `hmac_secret = "s"`)
	p, err := ibmbob.New(block, ibmbob.Options{})
	if err != nil {
		t.Fatal(err)
	}
	p.SetCachedSSO(&ibmbob.SSOToken{AccessToken: "cached", ExpiresAt: time.Now().Add(time.Hour)})

	// Reload must clear the in-memory cache.
	p.Reload()

	if got := p.CurrentAccessToken(); got != "" {
		t.Errorf("after Reload, CurrentAccessToken = %q, want empty", got)
	}
}
