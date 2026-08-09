package sidecar_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
	"github.com/thelonelyghost/llm-proxy/internal/backend/sidecar"
	"github.com/thelonelyghost/llm-proxy/internal/plugin"
)

// newTestSidecar starts a test HTTP server and returns a sidecar Backend
// pointing at it.
func newTestSidecar(t *testing.T, handler http.Handler) *sidecar.Backend {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	b, err := sidecar.New(sidecar.Options{
		Name:    "testplugin",
		BaseURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("sidecar.New: %v", err)
	}
	return b
}

// TestSidecar_Name verifies that the backend returns the configured name.
func TestSidecar_Name(t *testing.T) {
	b := newTestSidecar(t, http.NotFoundHandler())
	if got := b.Name(); got != "testplugin" {
		t.Errorf("Name() = %q, want %q", got, "testplugin")
	}
}

// TestSidecar_Chat_ForwardsRequest verifies that Chat proxies the request body
// to /v1/chat/completions and returns the upstream response.
func TestSidecar_Chat_ForwardsRequest(t *testing.T) {
	const responseBody = `{"id":"chatcmpl-1","choices":[]}`
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, responseBody)
	})
	b := newTestSidecar(t, handler)
	resp, err := b.Chat(context.Background(), backend.ChatRequest{
		Model: "testplugin/gpt-4o",
		Body:  []byte(`{"model":"gpt-4o","messages":[]}`),
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if string(got) != responseBody {
		t.Errorf("body = %q, want %q", got, responseBody)
	}
}

// TestSidecar_Complete_Unsupported verifies that Complete returns ErrUnsupported
// when the upstream responds with 501.
func TestSidecar_Complete_Unsupported(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
	})
	b := newTestSidecar(t, handler)
	_, err := b.Complete(context.Background(), backend.CompletionRequest{
		Model: "testplugin/gpt-4o",
		Body:  []byte(`{"model":"gpt-4o","prompt":"hi"}`),
	})
	if err == nil {
		t.Fatal("expected ErrUnsupported, got nil")
	}
	if !isUnsupported(err) {
		t.Errorf("expected ErrUnsupported, got %v", err)
	}
}

// TestSidecar_Embed_ForwardsRequest verifies that Embed proxies to /v1/embeddings.
func TestSidecar_Embed_ForwardsRequest(t *testing.T) {
	const responseBody = `{"object":"list","data":[]}`
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, responseBody)
	})
	b := newTestSidecar(t, handler)
	resp, err := b.Embed(context.Background(), backend.EmbeddingRequest{
		Model: "testplugin/text-embedding-3-small",
		Body:  []byte(`{"model":"text-embedding-3-small","input":"hello"}`),
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", resp.StatusCode)
	}
}

// TestSidecar_Models_ForwardsRequest verifies that Models calls GET /v1/models
// on the sidecar and decodes the response.
func TestSidecar_Models_ForwardsRequest(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"gpt-4o","owned_by":"openai"}]}`)
	})
	b := newTestSidecar(t, handler)
	models, err := b.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(models))
	}
	if models[0].ID != "gpt-4o" {
		t.Errorf("models[0].ID = %q, want %q", models[0].ID, "gpt-4o")
	}
}

// TestSidecar_Responses_ForwardsRequest verifies that Responses proxies to /v1/responses.
func TestSidecar_Responses_ForwardsRequest(t *testing.T) {
	const responseBody = `{"id":"resp-1","output":[]}`
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, responseBody)
	})
	b := newTestSidecar(t, handler)
	resp, err := b.Responses(context.Background(), backend.ResponsesRequest{
		Model: "testplugin/gpt-4o",
		Body:  []byte(`{"model":"gpt-4o","input":"hello"}`),
	})
	if err != nil {
		t.Fatalf("Responses: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", resp.StatusCode)
	}
}

// isUnsupported returns true if err wraps backend.ErrUnsupported.
func isUnsupported(err error) bool {
	return err != nil && strings.Contains(err.Error(), backend.ErrUnsupported.Error())
}

// newTestSidecarWithTools creates a sidecar.Backend with optional tool support.
func newTestSidecarWithTools(t *testing.T, handler http.Handler, tools []plugin.Tool, binPath string) *sidecar.Backend {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	b, err := sidecar.New(sidecar.Options{
		Name:       "testplugin",
		BaseURL:    srv.URL,
		BinaryPath: binPath,
		Tools:      tools,
	})
	if err != nil {
		t.Fatalf("sidecar.New: %v", err)
	}
	return b
}

// writeTinyScript writes a minimal shell/batch script that exits 0 and prints
// msg to stdout, returning its path. Skips the test on unsupported platforms.
func writeTinyScript(t *testing.T, msg string) string {
	t.Helper()
	dir := t.TempDir()
	var path string
	if runtime.GOOS == "windows" {
		path = filepath.Join(dir, "tool.bat")
		if err := os.WriteFile(path, []byte("@echo off\necho "+msg+"\n"), 0o755); err != nil { //nolint:gosec
			t.Fatal(err)
		}
	} else {
		path = filepath.Join(dir, "tool.sh")
		if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s' '"+msg+"'\n"), 0o755); err != nil { //nolint:gosec
			t.Fatal(err)
		}
	}
	return path
}

// TestSidecar_RunTool_ToolNotInManifest verifies that RunTool returns
// ErrToolNotFound when the tool name is absent from the manifest tools list.
func TestSidecar_RunTool_ToolNotInManifest(t *testing.T) {
	b := newTestSidecarWithTools(t, http.NotFoundHandler(),
		[]plugin.Tool{{Name: "login", Description: "auth flow"}},
		"/some/binary",
	)
	_, err := b.RunTool(context.Background(), "budget")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, backend.ErrToolNotFound) {
		t.Errorf("expected ErrToolNotFound, got: %v", err)
	}
}

// TestSidecar_RunTool_NoBinaryPath verifies that RunTool returns an error when
// the tool is registered but no binary path was provided.
func TestSidecar_RunTool_NoBinaryPath(t *testing.T) {
	b := newTestSidecarWithTools(t, http.NotFoundHandler(),
		[]plugin.Tool{{Name: "budget"}},
		"", // no binary
	)
	_, err := b.RunTool(context.Background(), "budget")
	if err == nil {
		t.Fatal("expected error for empty BinaryPath, got nil")
	}
}

// TestSidecar_RunTool_CapturesStdout verifies that RunTool captures the
// process's stdout and returns it as bytes.
func TestSidecar_RunTool_CapturesStdout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test skipped on Windows")
	}
	const payload = `{"object":"usage.budget","max_budget":10,"spend":1,"remaining":9}`
	binPath := writeTinyScript(t, payload)

	b := newTestSidecarWithTools(t, http.NotFoundHandler(),
		[]plugin.Tool{{Name: "budget"}},
		binPath,
	)
	out, err := b.RunTool(context.Background(), "budget")
	if err != nil {
		t.Fatalf("RunTool: %v", err)
	}
	if string(out) != payload {
		t.Errorf("stdout = %q, want %q", out, payload)
	}
}

// TestSidecar_RunTool_NonZeroExit returns an error (and any stdout captured)
// when the binary exits non-zero.
func TestSidecar_RunTool_NonZeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test skipped on Windows")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fail.sh")
	errPayload := `{"object":"usage.budget","error":"bad creds"}`
	if err := os.WriteFile(script,
		[]byte("#!/bin/sh\nprintf '%s' '"+errPayload+"'\nexit 1\n"), 0o755); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	b := newTestSidecarWithTools(t, http.NotFoundHandler(),
		[]plugin.Tool{{Name: "budget"}},
		script,
	)
	out, err := b.RunTool(context.Background(), "budget")
	if err == nil {
		t.Fatal("expected non-zero-exit error")
	}
	// stdout bytes should still be returned alongside the error.
	if string(out) != errPayload {
		t.Errorf("stdout = %q, want %q", out, errPayload)
	}
}

// TestSidecar_ImplementsToolRunner verifies the interface is satisfied.
func TestSidecar_ImplementsToolRunner(t *testing.T) {
	b, err := sidecar.New(sidecar.Options{Name: "x", BaseURL: "http://127.0.0.1:9999"})
	if err != nil {
		t.Fatal(err)
	}
	var _ backend.ToolRunner = b
}
