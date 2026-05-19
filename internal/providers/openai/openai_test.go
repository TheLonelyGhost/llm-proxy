package openai_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/thelonelyghost/llm-proxy/internal/backend"
	"github.com/thelonelyghost/llm-proxy/internal/config"
	"github.com/thelonelyghost/llm-proxy/internal/providers/openai"
)

func buildBlock(t *testing.T, body string) config.BackendBlock {
	t.Helper()
	full := fmt.Sprintf("backend \"openai\" {\n  type = \"openai\"\n  %s\n}\n", body)
	p := hclparse.NewParser()
	f, diags := p.ParseHCL([]byte(full), "test.hcl")
	if diags.HasErrors() {
		t.Fatalf("parse: %s", diags.Error())
	}
	cfg := struct {
		Backends []config.BackendBlock `hcl:"backend,block"`
		Rem      hcl.Body              `hcl:",remain"`
	}{}
	dir := t.TempDir()
	path := filepath.Join(dir, "c.hcl")
	if err := os.WriteFile(path, []byte(full), 0o600); err != nil {
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestNew_DefaultsAndOverrides(t *testing.T) {
	block := buildBlock(t, `api_key = "sk-test123"
models = ["gpt-4"]`)
	p, err := openai.New(block, openai.Options{
		HTTPClient: &http.Client{Timeout: 1 * time.Second},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.Name() != "openai" {
		t.Errorf("name = %q", p.Name())
	}
	models, _ := p.Models(context.Background())
	if len(models) != 1 || models[0].ID != "gpt-4" {
		t.Errorf("models = %v", models)
	}
}

func TestNew_CustomBaseURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"id": "cmpl-1", "object": "chat.completion"})
	}))
	defer srv.Close()

	block := buildBlock(t, fmt.Sprintf(`base_url = %q
models = ["custom-model"]`, srv.URL))
	p, err := openai.New(block, openai.Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	resp, err := p.Chat(context.Background(), backend.ChatRequest{
		Model: "custom-model",
		Body:  []byte(`{"model":"custom-model","messages":[]}`),
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestNew_NoAPIKeyDefaultBaseURL(t *testing.T) {
	block := buildBlock(t, `models = ["gpt-4"]`)
	_, err := openai.New(block, openai.Options{})
	if err == nil || !strings.Contains(err.Error(), "api_key is required") {
		t.Fatalf("expected api_key required error, got %v", err)
	}
}

func TestNew_NoAPIKeyCustomBaseURL(t *testing.T) {
	block := buildBlock(t, `base_url = "https://custom.local"
models = ["model"]`)
	p, err := openai.New(block, openai.Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p == nil {
		t.Fatal("expected provider, got nil")
	}
}

func TestNew_WrongType(t *testing.T) {
	block := config.BackendBlock{Name: "x", Type: "other"}
	if _, err := openai.New(block, openai.Options{}); err == nil {
		t.Fatal("expected wrong-type error")
	}
}

func TestNew_BadTimeout(t *testing.T) {
	block := buildBlock(t, `api_key = "sk-test"
request_timeout = "garbage"`)
	if _, err := openai.New(block, openai.Options{}); err == nil {
		t.Fatal("expected timeout parse error")
	}
}

func TestProvider_AllowList(t *testing.T) {
	block := buildBlock(t, `api_key = "sk-test"
models = ["gpt-4"]`)
	p, err := openai.New(block, openai.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Chat(context.Background(), backend.ChatRequest{Model: "claude"})
	if err == nil || !errors.Is(err, backend.ErrUnknownModel) {
		t.Fatalf("expected ErrUnknownModel, got %v", err)
	}
}

func TestProvider_Chat(t *testing.T) {
	var authHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		writeJSON(w, map[string]any{"id": "cmpl-1", "object": "chat.completion"})
	}))
	defer srv.Close()

	block := buildBlock(t, fmt.Sprintf(`api_key = "sk-test"
base_url = %q
models = ["gpt-4"]`, srv.URL))
	p, err := openai.New(block, openai.Options{})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := p.Chat(context.Background(), backend.ChatRequest{
		Model: "gpt-4",
		Body:  []byte(`{"model":"gpt-4","messages":[]}`),
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if authHeader != "Bearer sk-test" {
		t.Errorf("Authorization = %q", authHeader)
	}
}

func TestProvider_Complete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		writeJSON(w, map[string]any{"id": "cmpl-1"})
	}))
	defer srv.Close()

	block := buildBlock(t, fmt.Sprintf(`api_key = "sk-test"
base_url = %q
models = ["gpt-3.5-turbo-instruct"]`, srv.URL))
	p, err := openai.New(block, openai.Options{})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := p.Complete(context.Background(), backend.CompletionRequest{
		Model: "gpt-3.5-turbo-instruct",
		Body:  []byte(`{"model":"gpt-3.5-turbo-instruct","prompt":"hello"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestProvider_Embed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("path = %q", r.URL.Path)
		}
		writeJSON(w, map[string]any{"data": []any{}})
	}))
	defer srv.Close()

	block := buildBlock(t, fmt.Sprintf(`api_key = "sk-test"
base_url = %q
models = ["text-embedding-ada-002"]`, srv.URL))
	p, err := openai.New(block, openai.Options{})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := p.Embed(context.Background(), backend.EmbeddingRequest{
		Model: "text-embedding-ada-002",
		Body:  []byte(`{"model":"text-embedding-ada-002","input":"hello"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestProvider_NoAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected Authorization header: %q", r.Header.Get("Authorization"))
		}
		writeJSON(w, map[string]any{"id": "cmpl-1"})
	}))
	defer srv.Close()

	block := buildBlock(t, fmt.Sprintf(`base_url = %q
models = ["model"]`, srv.URL))
	p, err := openai.New(block, openai.Options{})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := p.Chat(context.Background(), backend.ChatRequest{
		Model: "model",
		Body:  []byte(`{"model":"model","messages":[]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestProvider_ListUpstreamModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q", r.URL.Path)
		}
		writeJSON(w, map[string]any{
			"object": "list",
			"data": []map[string]any{
				{"id": "gpt-4o", "object": "model", "created": 123, "owned_by": "openai"},
				{"id": "gpt-4o-mini", "object": "model", "created": 456, "owned_by": "openai"},
			},
		})
	}))
	defer srv.Close()

	block := buildBlock(t, fmt.Sprintf(`base_url = %q`, srv.URL))
	p, err := openai.New(block, openai.Options{})
	if err != nil {
		t.Fatal(err)
	}

	models, err := p.ListUpstreamModels(context.Background())
	if err != nil {
		t.Fatalf("ListUpstreamModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models len = %d", len(models))
	}
	if models[0].ID != "gpt-4o" || models[1].ID != "gpt-4o-mini" {
		t.Fatalf("models = %+v", models)
	}
	if models[0].Vendor != "OpenAI" {
		t.Fatalf("vendor = %q", models[0].Vendor)
	}
	if models[0].Extras["owned_by"] != "openai" || models[0].Extras["created"] != "123" {
		t.Fatalf("extras = %+v", models[0].Extras)
	}
}

func TestProvider_ListUpstreamModels_SortsAndIgnoresMissingIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"object": "list",
			"data": []map[string]any{
				{"id": "z-model"},
				{},
				{"id": "a-model"},
			},
		})
	}))
	defer srv.Close()

	block := buildBlock(t, fmt.Sprintf(`base_url = %q`, srv.URL))
	p, err := openai.New(block, openai.Options{})
	if err != nil {
		t.Fatal(err)
	}

	models, err := p.ListUpstreamModels(context.Background())
	if err != nil {
		t.Fatalf("ListUpstreamModels: %v", err)
	}
	if got := []string{models[0].ID, models[1].ID}; got[0] != "a-model" || got[1] != "z-model" {
		t.Fatalf("sorted ids = %v", got)
	}
}
