package copilot_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
	"github.com/thelonelyghost/llm-proxy/internal/config"
	"github.com/thelonelyghost/llm-proxy/internal/providers/copilot"
)

// buildBlock parses an HCL fragment as a config.BackendBlock for tests.
func buildBlock(t *testing.T, body string) config.BackendBlock {
	t.Helper()
	full := fmt.Sprintf("backend \"copilot\" {\n  type = \"github-copilot\"\n  %s\n}\n", body)
	p := hclparse.NewParser()
	f, diags := p.ParseHCL([]byte(full), "test.hcl")
	if diags.HasErrors() {
		t.Fatalf("parse: %s", diags.Error())
	}
	cfg := struct {
		Backends []config.BackendBlock `hcl:"backend,block"`
		Rem      hcl.Body              `hcl:",remain"`
	}{}
	// Round-trip through config.Load is overkill; reuse internal decode.
	dir := t.TempDir()
	path := filepath.Join(dir, "c.hcl")
	if err := writeAll(path, []byte(full)); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = cfg
	_ = f
	return loaded.Backends[0]
}

func writeAll(path string, b []byte) error {
	return paths_writeFile(path, b)
}

// paths_writeFile defers to os.WriteFile via a tiny indirection to keep the
// test file imports minimal.
var paths_writeFile = func(path string, b []byte) error {
	return os.WriteFile(path, b, 0o600)
}

// New_DefaultsAndOverrides verifies New populates Settings defaults.
func TestNew_DefaultsAndOverrides(t *testing.T) {
	dir := t.TempDir()
	store := copilot.NewFileTokenStoreAt(filepath.Join(dir, "cfg"), filepath.Join(dir, "cache"))

	block := buildBlock(t, `models = ["gpt-4o"]`)
	p, err := copilot.New(block, copilot.Options{
		TokenStore: store,
		HTTPClient: &http.Client{Timeout: 1 * time.Second},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.Name() != "copilot" {
		t.Errorf("name = %q", p.Name())
	}
	models, _ := p.Models(context.Background())
	if len(models) != 1 || models[0].ID != "gpt-4o" {
		t.Errorf("models = %v", models)
	}
}

func TestNew_WrongType(t *testing.T) {
	block := config.BackendBlock{Name: "x", Type: "other"}
	if _, err := copilot.New(block, copilot.Options{}); err == nil {
		t.Fatal("expected wrong-type error")
	}
}

func TestNew_BadTimeout(t *testing.T) {
	block := buildBlock(t, `request_timeout = "garbage"`)
	if _, err := copilot.New(block, copilot.Options{}); err == nil {
		t.Fatal("expected timeout parse error")
	}
}

func TestProvider_AllowList(t *testing.T) {
	dir := t.TempDir()
	store := copilot.NewFileTokenStoreAt(filepath.Join(dir, "cfg"), filepath.Join(dir, "cache"))
	block := buildBlock(t, `models = ["gpt-4o"]`)
	p, err := copilot.New(block, copilot.Options{TokenStore: store})
	if err != nil {
		t.Fatal(err)
	}
	// Disallowed model returns ErrUnknownModel before any HTTP call.
	_, err = p.Chat(context.Background(), backend.ChatRequest{Model: "claude"})
	if err == nil || !errors.Is(err, backend.ErrUnknownModel) {
		t.Fatalf("expected ErrUnknownModel, got %v", err)
	}
}

// fakeServer simulates GitHub login and api.github.com + copilot api.
func newFakeServer(t *testing.T) (githubLogin, githubAPI, copilotAPI *httptest.Server, exchanges *int32) {
	t.Helper()
	var ex int32
	exchanges = &ex

	loginMux := http.NewServeMux()
	var pollCount int32
	loginMux.HandleFunc("/login/device/code", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, map[string]any{
			"device_code":      "DEV123",
			"user_code":        "WXYZ-1234",
			"verification_uri": "https://example.test/device",
			"expires_in":       900,
			"interval":         0, // exercise default
		})
	})
	loginMux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		c := atomic.AddInt32(&pollCount, 1)
		switch c {
		case 1:
			writeJSON(w, map[string]any{"error": "authorization_pending"})
		case 2:
			writeJSON(w, map[string]any{"error": "slow_down", "interval": 0})
		default:
			writeJSON(w, map[string]any{
				"access_token": "gho_test_token",
				"token_type":   "bearer",
				"scope":        "read:user",
			})
		}
	})

	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/copilot_internal/v2/token", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&ex, 1)
		writeJSON(w, map[string]any{
			"token":      "cop_xyz",
			"expires_at": time.Now().Add(30 * time.Minute).Unix(),
		})
	})

	cpMux := http.NewServeMux()
	cpMux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cop_xyz" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, map[string]any{"id": "cmpl-1", "object": "chat.completion"})
	})

	githubLogin = httptest.NewServer(loginMux)
	githubAPI = httptest.NewServer(apiMux)
	copilotAPI = httptest.NewServer(cpMux)
	t.Cleanup(func() {
		githubLogin.Close()
		githubAPI.Close()
		copilotAPI.Close()
	})
	return
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestEndToEnd_LoginExchangeAndChat(t *testing.T) {
	loginSrv, apiSrv, cpSrv, exchanges := newFakeServer(t)

	dir := t.TempDir()
	store := copilot.NewFileTokenStoreAt(filepath.Join(dir, "cfg"), filepath.Join(dir, "cache"))

	block := buildBlock(t, fmt.Sprintf(`
        models                = ["gpt-4o"]
        api_base              = %q
        github_api_base       = %q
        github_login_base     = %q
        request_timeout       = "5s"
    `, cpSrv.URL, apiSrv.URL, loginSrv.URL))

	p, err := copilot.New(block, copilot.Options{TokenStore: store, IntervalUnit: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	// Drive the device-code flow.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, err := p.Auth().RequestDeviceCode(ctx)
	if err != nil {
		t.Fatalf("device code: %v", err)
	}
	if code.UserCode != "WXYZ-1234" {
		t.Errorf("user code = %q", code.UserCode)
	}
	if _, err := p.Auth().PollForToken(ctx, code); err != nil {
		t.Fatalf("poll: %v", err)
	}

	// Now issue a chat call - should exchange the token once and reuse.
	resp, err := p.Chat(ctx, backend.ChatRequest{
		Model: "gpt-4o",
		Body:  []byte(`{"model":"gpt-4o","messages":[]}`),
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}

	// Second chat - cached token, no additional exchange.
	resp2, err := p.Chat(ctx, backend.ChatRequest{
		Model: "gpt-4o",
		Body:  []byte(`{"model":"gpt-4o","messages":[]}`),
	})
	if err != nil {
		t.Fatalf("chat#2: %v", err)
	}
	resp2.Body.Close()
	if got := atomic.LoadInt32(exchanges); got != 1 {
		t.Errorf("exchanges = %d, want 1", got)
	}

	if err := p.Auth().Logout(); err != nil {
		t.Fatalf("logout: %v", err)
	}
}

func TestPollForToken_AccessDenied(t *testing.T) {
	loginMux := http.NewServeMux()
	loginMux.HandleFunc("/login/device/code", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"device_code": "x", "user_code": "y", "verification_uri": "z",
			"expires_in": 60, "interval": 0,
		})
	})
	loginMux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"error": "access_denied", "error_description": "user denied"})
	})
	srv := httptest.NewServer(loginMux)
	defer srv.Close()

	dir := t.TempDir()
	store := copilot.NewFileTokenStoreAt(filepath.Join(dir, "cfg"), filepath.Join(dir, "cache"))
	block := buildBlock(t, fmt.Sprintf(`github_login_base = %q`, srv.URL))
	p, errNew := copilot.New(block, copilot.Options{TokenStore: store, IntervalUnit: time.Millisecond})
	if errNew != nil {
		t.Fatal(errNew)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	code, err := p.Auth().RequestDeviceCode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Auth().PollForToken(ctx, code); err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("err = %v", err)
	}
}

func TestCopilotToken_RefreshOn401(t *testing.T) {
	// Issue 401 on first call, 200 on second.
	var calls int32
	cpMux := http.NewServeMux()
	cpMux.HandleFunc("/chat/completions", func(w http.ResponseWriter, _ *http.Request) {
		c := atomic.AddInt32(&calls, 1)
		if c == 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	cpSrv := httptest.NewServer(cpMux)
	defer cpSrv.Close()

	var exchanges int32
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/copilot_internal/v2/token", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&exchanges, 1)
		writeJSON(w, map[string]any{
			"token":      fmt.Sprintf("cop_%d", atomic.LoadInt32(&exchanges)),
			"expires_at": time.Now().Add(30 * time.Minute).Unix(),
		})
	})
	apiSrv := httptest.NewServer(apiMux)
	defer apiSrv.Close()

	dir := t.TempDir()
	store := copilot.NewFileTokenStoreAt(filepath.Join(dir, "cfg"), filepath.Join(dir, "cache"))
	if err := store.SaveGitHubToken(&copilot.GitHubToken{AccessToken: "gh", TokenType: "bearer"}); err != nil {
		t.Fatal(err)
	}

	block := buildBlock(t, fmt.Sprintf(`
        models          = ["gpt-4o"]
        api_base        = %q
        github_api_base = %q
    `, cpSrv.URL, apiSrv.URL))
	p, err := copilot.New(block, copilot.Options{TokenStore: store})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Chat(context.Background(), backend.ChatRequest{
		Model: "gpt-4o",
		Body:  []byte(`{"model":"gpt-4o"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if atomic.LoadInt32(&exchanges) != 2 {
		t.Errorf("exchanges = %d, want 2", exchanges)
	}
}

func TestCopilotToken_NotAuthenticated(t *testing.T) {
	dir := t.TempDir()
	store := copilot.NewFileTokenStoreAt(filepath.Join(dir, "cfg"), filepath.Join(dir, "cache"))
	block := buildBlock(t, `models = ["gpt-4o"]`)
	p, errNew := copilot.New(block, copilot.Options{TokenStore: store})
	if errNew != nil {
		t.Fatal(errNew)
	}

	_, err := p.Chat(context.Background(), backend.ChatRequest{
		Model: "gpt-4o",
		Body:  []byte(`{"model":"gpt-4o"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "not authenticated") {
		t.Fatalf("err = %v", err)
	}
}

// Sanity check that the form values reach the server unmangled.
func TestRequestDeviceCode_FormEncoded(t *testing.T) {
	var got url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("/login/device/code", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.PostForm
		writeJSON(w, map[string]any{
			"device_code":      "d",
			"user_code":        "u",
			"verification_uri": "v",
			"expires_in":       60,
			"interval":         5,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	store := copilot.NewFileTokenStoreAt(filepath.Join(dir, "cfg"), filepath.Join(dir, "cache"))
	block := buildBlock(t, fmt.Sprintf(`github_login_base = %q`, srv.URL))
	p, errNew := copilot.New(block, copilot.Options{TokenStore: store})
	if errNew != nil {
		t.Fatal(errNew)
	}

	if _, err := p.Auth().RequestDeviceCode(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.Get("client_id") != copilot.DefaultOAuthClientID {
		t.Errorf("client_id = %q", got.Get("client_id"))
	}
}

// TestCompleteAndEmbed exercises the two non-chat dispatch paths to ensure
// header injection and prefix-stripping work for them too.
func TestCompleteAndEmbed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/completions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"ok": "comp"})
	})
	mux.HandleFunc("/embeddings", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"ok": "emb"})
	})
	cpSrv := httptest.NewServer(mux)
	defer cpSrv.Close()

	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/copilot_internal/v2/token", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"token": "cop_xyz", "expires_at": time.Now().Add(time.Hour).Unix()})
	})
	apiSrv := httptest.NewServer(apiMux)
	defer apiSrv.Close()

	dir := t.TempDir()
	store := copilot.NewFileTokenStoreAt(filepath.Join(dir, "cfg"), filepath.Join(dir, "cache"))
	if err := store.SaveGitHubToken(&copilot.GitHubToken{AccessToken: "gh"}); err != nil {
		t.Fatal(err)
	}
	block := buildBlock(t, fmt.Sprintf(`
        api_base        = %q
        github_api_base = %q
    `, cpSrv.URL, apiSrv.URL))
	p, err := copilot.New(block, copilot.Options{TokenStore: store})
	if err != nil {
		t.Fatal(err)
	}

	r1, err := p.Complete(context.Background(), backend.CompletionRequest{
		Model: "gpt-4o", Body: []byte(`{"model":"gpt-4o","prompt":"hi"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	r1.Body.Close()
	if r1.StatusCode != 200 {
		t.Errorf("complete status = %d", r1.StatusCode)
	}

	r2, err := p.Embed(context.Background(), backend.EmbeddingRequest{
		Model: "text-emb", Body: []byte(`{"model":"text-emb","input":"hi"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != 200 {
		t.Errorf("embed status = %d", r2.StatusCode)
	}
}

// --- /v1/responses end-to-end against a fake Copilot upstream ---

func TestEndToEnd_Responses_NonStreaming(t *testing.T) {
	dir := t.TempDir()
	store := copilot.NewFileTokenStoreAt(filepath.Join(dir, "cfg"), filepath.Join(dir, "cache"))

	var receivedBody []byte
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/copilot_internal/v2/token", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"token": "cop_xyz", "expires_at": time.Now().Add(30 * time.Minute).Unix()})
	})
	cpMux := http.NewServeMux()
	cpMux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		writeJSON(w, map[string]any{
			"id":      "chatcmpl-1",
			"object":  "chat.completion",
			"created": 1700000000,
			"choices": []any{
				map[string]any{
					"index":         0,
					"message":       map[string]any{"role": "assistant", "content": "Hello!"},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5},
		})
	})

	apiSrv := httptest.NewServer(apiMux)
	cpSrv := httptest.NewServer(cpMux)
	t.Cleanup(func() { apiSrv.Close(); cpSrv.Close() })

	// Seed a valid GitHub OAuth token directly so we don't need to drive the
	// device-code flow.
	if err := store.SaveGitHubToken(&copilot.GitHubToken{AccessToken: "gho_test"}); err != nil {
		t.Fatal(err)
	}

	block := buildBlock(t, fmt.Sprintf(`
        models                = ["gpt-4o"]
        api_base              = %q
        github_api_base       = %q
        request_timeout       = "5s"
    `, cpSrv.URL, apiSrv.URL))
	p, err := copilot.New(block, copilot.Options{TokenStore: store, IntervalUnit: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := p.Responses(context.Background(), backend.ResponsesRequest{
		Model: "gpt-4o",
		Body:  []byte(`{"model":"gpt-4o","input":"Say hi","max_output_tokens":32}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	// Upstream received a /chat/completions body.
	var upstream map[string]any
	if err := json.Unmarshal(receivedBody, &upstream); err != nil {
		t.Fatal(err)
	}
	if upstream["max_tokens"] != float64(32) {
		t.Errorf("max_tokens not forwarded: %+v", upstream)
	}
	msgs := upstream["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["content"] != "Say hi" {
		t.Errorf("messages malformed: %+v", msgs)
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
	output := out["output"].([]any)
	if len(output) != 1 || output[0].(map[string]any)["type"] != "message" {
		t.Errorf("output not a message item: %+v", output)
	}
	usage := out["usage"].(map[string]any)
	if usage["input_tokens"] != float64(3) || usage["output_tokens"] != float64(2) {
		t.Errorf("usage mismapped: %+v", usage)
	}
}

func TestEndToEnd_Responses_Streaming(t *testing.T) {
	dir := t.TempDir()
	store := copilot.NewFileTokenStoreAt(filepath.Join(dir, "cfg"), filepath.Join(dir, "cache"))

	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/copilot_internal/v2/token", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"token": "cop_xyz", "expires_at": time.Now().Add(30 * time.Minute).Unix()})
	})
	cpMux := http.NewServeMux()
	cpMux.HandleFunc("/chat/completions", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		f, _ := w.(http.Flusher)
		writeSSE := func(payload string) {
			_, _ = io.WriteString(w, "data: "+payload+"\n\n")
			if f != nil {
				f.Flush()
			}
		}
		writeSSE(`{"id":"chatcmpl-s","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`)
		writeSSE(`{"id":"chatcmpl-s","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}`)
		writeSSE(`{"id":"chatcmpl-s","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		writeSSE(`{"id":"chatcmpl-s","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		writeSSE(`[DONE]`)
	})

	apiSrv := httptest.NewServer(apiMux)
	cpSrv := httptest.NewServer(cpMux)
	t.Cleanup(func() { apiSrv.Close(); cpSrv.Close() })

	if err := store.SaveGitHubToken(&copilot.GitHubToken{AccessToken: "gho_test"}); err != nil {
		t.Fatal(err)
	}
	block := buildBlock(t, fmt.Sprintf(`
        models                = ["gpt-4o"]
        api_base              = %q
        github_api_base       = %q
        request_timeout       = "5s"
    `, cpSrv.URL, apiSrv.URL))
	p, err := copilot.New(block, copilot.Options{TokenStore: store, IntervalUnit: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := p.Responses(context.Background(), backend.ResponsesRequest{
		Model:  "gpt-4o",
		Stream: true,
		Body:   []byte(`{"model":"gpt-4o","input":"hi","stream":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q", ct)
	}

	body, _ := io.ReadAll(resp.Body)
	got := string(body)
	wantSequence := []string{
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
	prev := 0
	for _, want := range wantSequence {
		idx := strings.Index(got[prev:], want)
		if idx < 0 {
			t.Fatalf("missing or out-of-order event %q. Stream:\n%s", want, got)
		}
		prev += idx + len(want)
	}
	// Final completed event should carry usage.
	if !strings.Contains(got, `"input_tokens":1`) || !strings.Contains(got, `"output_tokens":1`) {
		t.Errorf("usage not in completed event:\n%s", got)
	}
}
