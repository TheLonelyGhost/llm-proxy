package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
	"github.com/thelonelyghost/llm-proxy/internal/server"
)

// fakeBackend implements backend.Backend with scripted responses.
type fakeBackend struct {
	name    string
	models  []backend.ModelInfo
	respond func(method string, body []byte) (*backend.Response, error)

	mu       sync.Mutex
	captured map[string][]byte
}

func newFakeBackend(name string, models []string) *fakeBackend {
	ms := make([]backend.ModelInfo, 0, len(models))
	for _, m := range models {
		ms = append(ms, backend.ModelInfo{ID: m, Vendor: name})
	}
	return &fakeBackend{name: name, models: ms, captured: make(map[string][]byte)}
}

func (f *fakeBackend) Name() string                                        { return f.name }
func (f *fakeBackend) Models(context.Context) ([]backend.ModelInfo, error) { return f.models, nil }

func (f *fakeBackend) record(kind string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.captured[kind] = body
}
func (f *fakeBackend) get(kind string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.captured[kind]
}

func (f *fakeBackend) Chat(_ context.Context, r backend.ChatRequest) (*backend.Response, error) {
	f.record("chat", r.Body)
	return f.respond("chat", r.Body)
}
func (f *fakeBackend) Complete(_ context.Context, r backend.CompletionRequest) (*backend.Response, error) {
	f.record("completion", r.Body)
	return f.respond("completion", r.Body)
}
func (f *fakeBackend) Embed(_ context.Context, r backend.EmbeddingRequest) (*backend.Response, error) {
	f.record("embedding", r.Body)
	return f.respond("embedding", r.Body)
}
func (f *fakeBackend) Responses(_ context.Context, r backend.ResponsesRequest) (*backend.Response, error) {
	f.record("responses", r.Body)
	return f.respond("responses", r.Body)
}

func okResponse(body string) func(string, []byte) (*backend.Response, error) {
	return func(string, []byte) (*backend.Response, error) {
		return &backend.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	}
}

// startServer runs the proxy on a free TCP port and returns the base URL plus
// a cancel func.
func startServer(t *testing.T, b *fakeBackend) string {
	t.Helper()
	reg := backend.NewRegistry()
	if err := reg.Register(b); err != nil {
		t.Fatal(err)
	}
	srv := server.New(server.Options{Address: "127.0.0.1", Port: 0, Registry: reg})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return "http://" + ln.Addr().String()
}

func TestEndToEnd(t *testing.T) {
	b := newFakeBackend("copilot", []string{"gpt-4o"})
	b.respond = okResponse(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"test"},"finish_reason":"stop"}],"created":1234567890}`)
	base := startServer(t, b)

	t.Run("health", func(t *testing.T) {
		resp, err := http.Get(base + "/healthz") //nolint:gosec
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})

	t.Run("models", func(t *testing.T) {
		resp, err := http.Get(base + "/v1/models") //nolint:gosec
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out struct {
			Data []struct {
				ID string `json:"id"`
			}
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if len(out.Data) != 1 || out.Data[0].ID != "copilot/gpt-4o" {
			t.Fatalf("data = %+v", out.Data)
		}
	})

	t.Run("chat ok rewrites model", func(t *testing.T) {
		req := strings.NewReader(`{"model":"copilot/gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
		resp, err := http.Post(base+"/v1/chat/completions", "application/json", req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		var captured map[string]any
		if err := json.Unmarshal(b.get("chat"), &captured); err != nil {
			t.Fatal(err)
		}
		if captured["model"] != "gpt-4o" {
			t.Errorf("model = %v, want gpt-4o", captured["model"])
		}
	})

	t.Run("chat bad model -> 400", func(t *testing.T) {
		req := strings.NewReader(`{"model":"no-prefix","messages":[]}`)
		resp, err := http.Post(base+"/v1/chat/completions", "application/json", req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})

	t.Run("chat unknown backend -> 404", func(t *testing.T) {
		req := strings.NewReader(`{"model":"otherbackend/m","messages":[]}`)
		resp, err := http.Post(base+"/v1/chat/completions", "application/json", req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})

	t.Run("chat invalid JSON -> 400", func(t *testing.T) {
		resp, err := http.Post(base+"/v1/chat/completions", "application/json", strings.NewReader(`{bogus`))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})

	t.Run("chat missing model -> 400", func(t *testing.T) {
		resp, err := http.Post(base+"/v1/chat/completions", "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})

	t.Run("completion routes", func(t *testing.T) {
		b.respond = okResponse(`{"id":"cmpl-1","object":"text_completion","choices":[{"index":0,"text":"test","finish_reason":"stop"}],"created":1234567890}`)
		req := strings.NewReader(`{"model":"copilot/gpt-4o","prompt":"hi"}`)
		resp, err := http.Post(base+"/v1/completions", "application/json", req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})

	t.Run("embedding unsupported -> 501", func(t *testing.T) {
		b.respond = func(kind string, _ []byte) (*backend.Response, error) {
			if kind == "embedding" {
				return nil, backend.ErrUnsupported
			}
			return okResponse(`{}`)(kind, nil)
		}
		req := strings.NewReader(`{"model":"copilot/embed","input":"hi"}`)
		resp, err := http.Post(base+"/v1/embeddings", "application/json", req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 501 {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})

	t.Run("chat upstream error -> 502", func(t *testing.T) {
		b.respond = func(string, []byte) (*backend.Response, error) {
			return nil, errors.New("boom")
		}
		req := strings.NewReader(`{"model":"copilot/gpt-4o","messages":[]}`)
		resp, err := http.Post(base+"/v1/chat/completions", "application/json", req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 502 {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})

	t.Run("unknown model -> 404", func(t *testing.T) {
		b.respond = func(string, []byte) (*backend.Response, error) {
			return nil, backend.ErrUnknownModel
		}
		req := strings.NewReader(`{"model":"copilot/whatever","messages":[]}`)
		resp, err := http.Post(base+"/v1/chat/completions", "application/json", req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})

	t.Run("nil response -> 502", func(t *testing.T) {
		b.respond = func(string, []byte) (*backend.Response, error) { return nil, nil }
		req := strings.NewReader(`{"model":"copilot/gpt-4o","messages":[]}`)
		resp, err := http.Post(base+"/v1/chat/completions", "application/json", req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 502 {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})

	t.Run("streaming chat flushes", func(t *testing.T) {
		chunks := "data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
		b.respond = func(string, []byte) (*backend.Response, error) {
			return &backend.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "Connection": []string{"keep-alive"}},
				Body:       io.NopCloser(strings.NewReader(chunks)),
			}, nil
		}
		req := strings.NewReader(`{"model":"copilot/gpt-4o","stream":true,"messages":[]}`)
		resp, err := http.Post(base+"/v1/chat/completions", "application/json", req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)

		if !strings.Contains(string(body), "data:") {
			t.Errorf("expected SSE events in body, got: %q", string(body))
		}
		if !strings.Contains(string(body), "[DONE]") {
			t.Errorf("expected [DONE] event in body, got: %q", string(body))
		}
		if !strings.Contains(string(body), "copilot/gpt-4o") {
			t.Errorf("expected model prefix to be preserved, got: %q", string(body))
		}
	})
}

func TestRun_ListenError(t *testing.T) {
	// Bind to a deliberately bad port to provoke a listen error.
	reg := backend.NewRegistry()
	srv := server.New(server.Options{Address: "127.0.0.1", Port: -1, Registry: reg})
	err := srv.Run(context.Background())
	if err == nil {
		t.Fatal("expected listen error")
	}
}

func TestRun_GracefulShutdown(t *testing.T) {
	reg := backend.NewRegistry()
	srv := server.New(server.Options{Address: "127.0.0.1", Port: 0, Registry: reg})
	if !strings.Contains(srv.Addr(), "127.0.0.1") {
		t.Errorf("Addr = %q", srv.Addr())
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	// Give Run time to actually start listening before cancelling.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v", err)
		}
	case <-context.Background().Done():
	}
}

func TestHandleModels_OpenAISpecCompliance(t *testing.T) {
	b := newFakeBackend("test", []string{"model-a", "model-b"})
	baseURL := startServer(t, b)

	resp, err := http.Get(baseURL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}

	if result["object"] != "list" {
		t.Errorf("expected object=list, got %v", result["object"])
	}

	data, ok := result["data"].([]any)
	if !ok {
		t.Fatalf("data field is not an array")
	}

	if len(data) != 2 {
		t.Fatalf("expected 2 models, got %d", len(data))
	}

	for _, item := range data {
		model, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("model item is not an object")
		}

		if _, ok := model["id"]; !ok {
			t.Error("missing required field: id")
		}
		if model["object"] != "model" {
			t.Errorf("expected object=model, got %v", model["object"])
		}
		if _, ok := model["created"]; !ok {
			t.Error("missing required field: created")
		}
		if _, ok := model["owned_by"]; !ok {
			t.Error("missing required field: owned_by")
		}

		if _, ok := model["capabilities"]; ok {
			t.Error("unexpected field 'capabilities' in /v1/models response")
		}
		if _, ok := model["token_limits"]; ok {
			t.Error("unexpected field 'token_limits' in /v1/models response")
		}
		if _, ok := model["reasoning_effort_levels"]; ok {
			t.Error("unexpected field 'reasoning_effort_levels' in /v1/models response")
		}
	}
}

func TestHandleModels_EmptyBackends(t *testing.T) {
	reg := backend.NewRegistry()
	srv := server.New(server.Options{Address: "127.0.0.1", Port: 0, Registry: reg})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	baseURL := "http://" + ln.Addr().String()

	resp, err := http.Get(baseURL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	expected := `{"object":"list","data":[]}`
	var expectedJSON, actualJSON map[string]any
	if err := json.Unmarshal([]byte(expected), &expectedJSON); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &actualJSON); err != nil {
		t.Fatal(err)
	}

	if actualJSON["object"] != "list" {
		t.Errorf("expected object=list, got %v", actualJSON["object"])
	}

	data, ok := actualJSON["data"].([]any)
	if !ok {
		t.Fatalf("data is not an array, got %T: %v", actualJSON["data"], actualJSON["data"])
	}

	if len(data) != 0 {
		t.Errorf("expected empty data array, got %d items", len(data))
	}
}

func TestHandleModelInfo_EmptyBackends(t *testing.T) {
	reg := backend.NewRegistry()
	srv := server.New(server.Options{Address: "127.0.0.1", Port: 0, Registry: reg})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	baseURL := "http://" + ln.Addr().String()

	resp, err := http.Get(baseURL + "/v1/model/info")
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	var actualJSON map[string]any
	if err := json.Unmarshal(body, &actualJSON); err != nil {
		t.Fatal(err)
	}

	if actualJSON["object"] != "list" {
		t.Errorf("expected object=list, got %v", actualJSON["object"])
	}

	data, ok := actualJSON["data"].([]any)
	if !ok {
		t.Fatalf("data is not an array, got %T: %v", actualJSON["data"], actualJSON["data"])
	}

	if len(data) != 0 {
		t.Errorf("expected empty data array, got %d items", len(data))
	}
}

func TestHandleResponses_ValidRequest(t *testing.T) {
	b := newFakeBackend("openai", []string{"o1-preview"})
	b.respond = okResponse(`{
		"id": "resp-123",
		"object": "response",
		"choices": [{"message": {"role": "assistant", "content": "test"}}]
	}`)
	baseURL := startServer(t, b)

	reqBody := `{"model":"openai/o1-preview","input":"What is 2+2?","reasoning":{"effort":"medium"}}`
	resp, err := http.Post(baseURL+"/v1/responses", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	captured := b.get("responses")
	if captured == nil {
		t.Fatal("backend did not receive request")
	}

	var capturedReq map[string]any
	if err := json.Unmarshal(captured, &capturedReq); err != nil {
		t.Fatal(err)
	}

	if capturedReq["input"] != "What is 2+2?" {
		t.Errorf("expected input field to be preserved, got %v", capturedReq["input"])
	}

	if capturedReq["model"] != "o1-preview" {
		t.Errorf("expected model to be rewritten to o1-preview, got %v", capturedReq["model"])
	}
}

func TestHandleResponses_MissingInput(t *testing.T) {
	b := newFakeBackend("openai", []string{"o1"})
	baseURL := startServer(t, b)

	reqBody := `{"model":"openai/o1"}`
	resp, err := http.Post(baseURL+"/v1/responses", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	var errResp map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatal(err)
	}

	errObj, ok := errResp["error"].(map[string]any)
	if !ok {
		t.Fatal("missing error object")
	}

	msg, _ := errObj["message"].(string)
	if !strings.Contains(msg, "input") {
		t.Errorf("expected error about missing input, got: %s", msg)
	}
}

func TestHandleResponses_ArrayInput(t *testing.T) {
	b := newFakeBackend("openai", []string{"o1-preview"})
	b.respond = okResponse(`{"id":"resp-1","object":"response","choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	baseURL := startServer(t, b)

	const arrayInput = `[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]`
	reqBody := `{"model":"openai/o1-preview","input":` + arrayInput + `}`
	resp, err := http.Post(baseURL+"/v1/responses", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	captured := b.get("responses")
	if captured == nil {
		t.Fatal("backend did not receive request")
	}

	var capturedReq map[string]json.RawMessage
	if err := json.Unmarshal(captured, &capturedReq); err != nil {
		t.Fatal(err)
	}

	// Compare semantically (json.Marshal of map[string]any may reorder keys).
	var gotItems, wantItems []map[string]any
	if err := json.Unmarshal(capturedReq["input"], &gotItems); err != nil {
		t.Fatalf("forwarded input is not an array of objects: %v (raw=%s)", err, capturedReq["input"])
	}
	if err := json.Unmarshal([]byte(arrayInput), &wantItems); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%v", gotItems) != fmt.Sprintf("%v", wantItems) {
		t.Errorf("input array not forwarded:\n got=%v\nwant=%v", gotItems, wantItems)
	}
}

func TestHandleResponses_RejectsWrongKindInput(t *testing.T) {
	b := newFakeBackend("openai", []string{"o1"})
	baseURL := startServer(t, b)

	cases := []struct {
		name    string
		reqBody string
	}{
		{"null", `{"model":"openai/o1","input":null}`},
		{"empty string", `{"model":"openai/o1","input":""}`},
		{"number", `{"model":"openai/o1","input":42}`},
		{"bool", `{"model":"openai/o1","input":true}`},
		{"object", `{"model":"openai/o1","input":{"role":"user"}}`},
		{"array of non-objects", `{"model":"openai/o1","input":[1,2,3]}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Post(baseURL+"/v1/responses", "application/json", strings.NewReader(tc.reqBody))
			if err != nil {
				t.Fatal(err)
			}
			if resp == nil {
				t.Fatal("nil response")
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("expected 400, got %d, body=%s", resp.StatusCode, body)
			}
		})
	}
}

func TestHandleResponses_InvalidReasoningEffort(t *testing.T) {
	b := newFakeBackend("openai", []string{"o1"})
	baseURL := startServer(t, b)

	tests := []struct {
		name      string
		reqBody   string
		shouldErr bool
	}{
		{
			name:      "valid low",
			reqBody:   `{"model":"openai/o1","input":"test","reasoning":{"effort":"low"}}`,
			shouldErr: false,
		},
		{
			name:      "valid medium",
			reqBody:   `{"model":"openai/o1","input":"test","reasoning":{"effort":"medium"}}`,
			shouldErr: false,
		},
		{
			name:      "valid high",
			reqBody:   `{"model":"openai/o1","input":"test","reasoning":{"effort":"high"}}`,
			shouldErr: false,
		},
		{
			name:      "valid minimal",
			reqBody:   `{"model":"openai/o1","input":"test","reasoning":{"effort":"minimal"}}`,
			shouldErr: false,
		},
		{
			name:      "invalid value",
			reqBody:   `{"model":"openai/o1","input":"test","reasoning":{"effort":"ultra"}}`,
			shouldErr: true,
		},
		{
			name:      "empty reasoning object",
			reqBody:   `{"model":"openai/o1","input":"test","reasoning":{}}`,
			shouldErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.shouldErr {
				b.respond = okResponse(`{"id":"resp-1","choices":[]}`)
			}

			resp, err := http.Post(baseURL+"/v1/responses", "application/json", strings.NewReader(tt.reqBody))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			if tt.shouldErr {
				if resp.StatusCode != http.StatusBadRequest {
					body, _ := io.ReadAll(resp.Body)
					t.Fatalf("expected 400 for %s, got %d, body: %s", tt.name, resp.StatusCode, body)
				}

				var errResp map[string]any
				if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
					t.Fatal(err)
				}

				errObj, ok := errResp["error"].(map[string]any)
				if !ok {
					t.Fatal("missing error object")
				}

				msg, _ := errObj["message"].(string)
				if !strings.Contains(msg, "reasoning.effort") {
					t.Errorf("expected error about reasoning.effort, got: %s", msg)
				}
			} else if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("expected 200 for %s, got %d, body: %s", tt.name, resp.StatusCode, body)
			}
		})
	}
}

func TestHandleResponses_ErrUnsupported(t *testing.T) {
	b := newFakeBackend("copilot", []string{"gpt-4o"})
	b.respond = func(string, []byte) (*backend.Response, error) {
		return nil, backend.ErrUnsupported
	}
	baseURL := startServer(t, b)

	reqBody := `{"model":"copilot/gpt-4o","input":"test"}`
	resp, err := http.Post(baseURL+"/v1/responses", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", resp.StatusCode)
	}

	var errResp map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatal(err)
	}

	errObj, ok := errResp["error"].(map[string]any)
	if !ok {
		t.Fatal("missing error object")
	}

	errType, _ := errObj["type"].(string)
	if errType != "not_implemented" {
		t.Errorf("expected error type not_implemented, got: %s", errType)
	}
}

func TestHandleModelInfo_WithCapabilities(t *testing.T) {
	b := newFakeBackend("openai", []string{"gpt-4o", "o1-preview"})
	b.models = []backend.ModelInfo{
		{
			ID:           "gpt-4o",
			Name:         "GPT-4o",
			Vendor:       "openai",
			Capabilities: []string{"chat", "tools", "vision"},
			TokenLimits: &backend.TokenLimits{
				MaxInputTokens:   128000,
				MaxOutputTokens:  16384,
				MaxContextTokens: 128000,
			},
		},
		{
			ID:                    "o1-preview",
			Name:                  "o1-preview",
			Vendor:                "openai",
			Capabilities:          []string{"chat", "reasoning"},
			ReasoningEffortLevels: []string{"low", "medium", "high"},
			TokenLimits: &backend.TokenLimits{
				MaxInputTokens:   128000,
				MaxOutputTokens:  32768,
				MaxContextTokens: 128000,
			},
		},
	}

	baseURL := startServer(t, b)

	resp, err := http.Get(baseURL + "/v1/model/info")
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}

	if result["object"] != "list" {
		t.Errorf("expected object=list, got %v", result["object"])
	}

	data, ok := result["data"].([]any)
	if !ok {
		t.Fatalf("data field is not an array")
	}

	if len(data) != 2 {
		t.Fatalf("expected 2 models, got %d", len(data))
	}

	gpt4o := data[0].(map[string]any)
	if gpt4o["id"] != "openai/gpt-4o" {
		t.Errorf("expected id=openai/gpt-4o, got %v", gpt4o["id"])
	}
	if gpt4o["vendor"] != "openai" {
		t.Errorf("expected vendor=openai, got %v", gpt4o["vendor"])
	}

	caps, ok := gpt4o["capabilities"].([]any)
	if !ok || len(caps) != 3 {
		t.Errorf("expected 3 capabilities for gpt-4o, got %v", gpt4o["capabilities"])
	}

	tokenLimits, ok := gpt4o["token_limits"].(map[string]any)
	if !ok {
		t.Fatal("token_limits missing or invalid")
	}
	if tokenLimits["max_input_tokens"] != float64(128000) {
		t.Errorf("expected max_input_tokens=128000, got %v", tokenLimits["max_input_tokens"])
	}

	o1 := data[1].(map[string]any)
	if o1["id"] != "openai/o1-preview" {
		t.Errorf("expected id=openai/o1-preview, got %v", o1["id"])
	}

	reasoningLevels, ok := o1["reasoning_effort_levels"].([]any)
	if !ok || len(reasoningLevels) != 3 {
		t.Errorf("expected 3 reasoning effort levels for o1-preview, got %v", o1["reasoning_effort_levels"])
	}
}

func TestModelCapabilityDifferences(t *testing.T) {
	copilotBackend := newFakeBackend("copilot", []string{"gpt-4o"})
	copilotBackend.models = []backend.ModelInfo{
		{
			ID:           "gpt-4o",
			Name:         "GPT-4o",
			Vendor:       "github",
			Capabilities: []string{"chat", "tools", "streaming"},
			TokenLimits: &backend.TokenLimits{
				MaxInputTokens:   128000,
				MaxOutputTokens:  4096,
				MaxContextTokens: 128000,
			},
		},
	}

	openaiBackend := newFakeBackend("openai", []string{"gpt-4o"})
	openaiBackend.models = []backend.ModelInfo{
		{
			ID:           "gpt-4o",
			Name:         "GPT-4o",
			Vendor:       "openai",
			Capabilities: []string{"chat", "tools", "vision", "streaming"},
			TokenLimits: &backend.TokenLimits{
				MaxInputTokens:   128000,
				MaxOutputTokens:  16384,
				MaxContextTokens: 128000,
			},
		},
	}

	reg := backend.NewRegistry()
	if err := reg.Register(copilotBackend); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(openaiBackend); err != nil {
		t.Fatal(err)
	}

	srv := server.New(server.Options{Address: "127.0.0.1", Port: 0, Registry: reg})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	baseURL := "http://" + ln.Addr().String()

	resp, err := http.Get(baseURL + "/v1/model/info")
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}

	data, ok := result["data"].([]any)
	if !ok {
		t.Fatalf("data field is not an array")
	}

	if len(data) != 2 {
		t.Fatalf("expected 2 models, got %d", len(data))
	}

	var copilotModel, openaiModel map[string]any
	for _, item := range data {
		model := item.(map[string]any)
		id := model["id"].(string)
		switch id {
		case "copilot/gpt-4o":
			copilotModel = model
		case "openai/gpt-4o":
			openaiModel = model
		}
	}

	if copilotModel == nil || openaiModel == nil {
		t.Fatal("missing expected models in response")
	}

	copilotCaps := copilotModel["capabilities"].([]any)
	openaiCaps := openaiModel["capabilities"].([]any)

	if len(openaiCaps) <= len(copilotCaps) {
		t.Error("expected openai/gpt-4o to have more capabilities than copilot/gpt-4o")
	}

	hasVision := false
	for _, cap := range openaiCaps {
		if cap == "vision" {
			hasVision = true
			break
		}
	}
	if !hasVision {
		t.Error("expected openai/gpt-4o to have vision capability")
	}

	for _, cap := range copilotCaps {
		if cap == "vision" {
			t.Error("copilot/gpt-4o should not have vision capability")
		}
	}

	copilotLimits := copilotModel["token_limits"].(map[string]any)
	openaiLimits := openaiModel["token_limits"].(map[string]any)

	if copilotLimits["max_output_tokens"].(float64) != 4096 {
		t.Errorf("expected copilot/gpt-4o max_output_tokens=4096, got %v", copilotLimits["max_output_tokens"])
	}

	if openaiLimits["max_output_tokens"].(float64) != 16384 {
		t.Errorf("expected openai/gpt-4o max_output_tokens=16384, got %v", openaiLimits["max_output_tokens"])
	}
}

func TestTransformNonConformantBackendResponses(t *testing.T) {
	tests := []struct {
		name         string
		endpoint     string
		reqBody      string
		backendBody  string
		expectStatus int
	}{
		{
			name:     "chat missing choices array",
			endpoint: "/v1/chat/completions",
			reqBody:  `{"model":"test/gpt-4o","messages":[{"role":"user","content":"hi"}]}`,
			backendBody: `{
				"id": "chat-123",
				"object": "chat.completion"
			}`,
			expectStatus: 502,
		},
		{
			name:     "completion missing choices",
			endpoint: "/v1/completions",
			reqBody:  `{"model":"test/gpt-4o","prompt":"hi"}`,
			backendBody: `{
				"id": "cmpl-123",
				"object": "text_completion"
			}`,
			expectStatus: 502,
		},
		{
			name:     "embedding missing data array",
			endpoint: "/v1/embeddings",
			reqBody:  `{"model":"test/embed","input":"test"}`,
			backendBody: `{
				"object": "list"
			}`,
			expectStatus: 502,
		},
		{
			name:     "responses missing choices",
			endpoint: "/v1/responses",
			reqBody:  `{"model":"test/o1","input":"test"}`,
			backendBody: `{
				"id": "resp-123",
				"object": "response"
			}`,
			expectStatus: 502,
		},
		{
			name:         "invalid JSON from backend",
			endpoint:     "/v1/chat/completions",
			reqBody:      `{"model":"test/gpt-4o","messages":[]}`,
			backendBody:  `{invalid json that cannot be parsed`,
			expectStatus: 502,
		},
		{
			name:         "plain text response from backend",
			endpoint:     "/v1/chat/completions",
			reqBody:      `{"model":"test/gpt-4o","messages":[]}`,
			backendBody:  `Internal Server Error`,
			expectStatus: 502,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newFakeBackend("test", []string{"gpt-4o", "embed", "o1"})
			b.respond = func(string, []byte) (*backend.Response, error) {
				return &backend.Response{
					StatusCode: 200,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(tt.backendBody)),
				}, nil
			}
			baseURL := startServer(t, b)

			resp, err := http.Post(baseURL+tt.endpoint, "application/json", strings.NewReader(tt.reqBody))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tt.expectStatus {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("expected status %d, got %d, body: %s", tt.expectStatus, resp.StatusCode, body)
			}

			var errResp map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
				t.Fatal(err)
			}

			if _, ok := errResp["error"]; !ok {
				t.Error("expected error object in response")
			}
		})
	}
}

// --- Body logging carve-out tests ---

func startServerWithLogging(t *testing.T, b *fakeBackend, logBodies bool, level slog.Level) (string, *bytes.Buffer) {
	t.Helper()
	reg := backend.NewRegistry()
	if err := reg.Register(b); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: level}))
	srv := server.New(server.Options{
		Address: "127.0.0.1", Port: 0,
		Registry:  reg,
		Logger:    logger,
		LogBodies: logBodies,
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return "http://" + ln.Addr().String(), &buf
}

func TestBodyLogging_OffByDefault(t *testing.T) {
	b := newFakeBackend("copilot", []string{"gpt-4o"})
	b.respond = okResponse(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"created":1}`)
	baseURL, logs := startServerWithLogging(t, b, false, slog.LevelDebug)

	const reqBody = `{"model":"copilot/gpt-4o","messages":[{"role":"user","content":"secret prompt"}]}`
	resp, err := http.Post(baseURL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck

	out := logs.String()
	if strings.Contains(out, "secret prompt") {
		t.Errorf("logs leaked request body when LogBodies=false:\n%s", out)
	}
	if strings.Contains(out, "request bodies") {
		t.Errorf("body log line emitted when LogBodies=false:\n%s", out)
	}
}

func TestBodyLogging_OnAtDebug_LogsRequestAndResponse(t *testing.T) {
	b := newFakeBackend("copilot", []string{"gpt-4o"})
	const respBody = `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi-from-upstream"},"finish_reason":"stop"}],"created":1}`
	b.respond = okResponse(respBody)
	baseURL, logs := startServerWithLogging(t, b, true, slog.LevelDebug)

	const reqBody = `{"model":"copilot/gpt-4o","messages":[{"role":"user","content":"prompt-from-client"}]}`
	resp, err := http.Post(baseURL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck

	out := logs.String()
	if !strings.Contains(out, "prompt-from-client") {
		t.Errorf("expected request body in logs, got:\n%s", out)
	}
	if !strings.Contains(out, "hi-from-upstream") {
		t.Errorf("expected response body in logs, got:\n%s", out)
	}
	if !strings.Contains(out, "request bodies") {
		t.Errorf("expected DEBUG body log line, got:\n%s", out)
	}
}

func TestBodyLogging_OnButInfoLevel_DoesNotLog(t *testing.T) {
	b := newFakeBackend("copilot", []string{"gpt-4o"})
	b.respond = okResponse(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"created":1}`)
	baseURL, logs := startServerWithLogging(t, b, true, slog.LevelInfo)

	const reqBody = `{"model":"copilot/gpt-4o","messages":[{"role":"user","content":"sensitive-prompt"}]}`
	resp, err := http.Post(baseURL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck

	out := logs.String()
	if strings.Contains(out, "sensitive-prompt") {
		t.Errorf("logs leaked request body at INFO level:\n%s", out)
	}
}

func TestBodyLogging_StreamingResponse_NotCaptured(t *testing.T) {
	b := newFakeBackend("copilot", []string{"gpt-4o"})
	const streamBody = `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\ndata: [DONE]\n\n"
	b.respond = func(string, []byte) (*backend.Response, error) {
		return &backend.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(streamBody)),
		}, nil
	}
	baseURL, logs := startServerWithLogging(t, b, true, slog.LevelDebug)

	const reqBody = `{"model":"copilot/gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	resp, err := http.Post(baseURL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck

	out := logs.String()
	if !strings.Contains(out, "streaming: not captured") {
		t.Errorf("expected streaming response to be marked not-captured, got:\n%s", out)
	}
}
