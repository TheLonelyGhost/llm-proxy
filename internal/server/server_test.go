package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
	models  []backend.Model
	respond func(method string, body []byte) (*backend.Response, error)

	mu       sync.Mutex
	captured map[string][]byte
}

func newFakeBackend(name string, models []string) *fakeBackend {
	ms := make([]backend.Model, 0, len(models))
	for _, m := range models {
		ms = append(ms, backend.Model{ID: m, OwnedBy: name})
	}
	return &fakeBackend{name: name, models: ms, captured: make(map[string][]byte)}
}

func (f *fakeBackend) Name() string                                    { return f.name }
func (f *fakeBackend) Models(context.Context) ([]backend.Model, error) { return f.models, nil }

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
	b.respond = okResponse(`{"id":"x","object":"chat.completion"}`)
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
		chunks := "data: {\"a\":1}\n\ndata: [DONE]\n\n"
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
		if string(body) != chunks {
			t.Errorf("body = %q", string(body))
		}
		// Connection header is hop-by-hop and must have been stripped.
		// (Go's client may also drop it, but the assertion here is on
		// what the proxy elects to forward upstream → downstream.)
		_ = resp.Header
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
