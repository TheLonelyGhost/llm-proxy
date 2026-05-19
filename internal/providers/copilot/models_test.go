package copilot_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
	"github.com/thelonelyghost/llm-proxy/internal/providers/copilot"
)

// TestListUpstreamModels verifies the upstream /models endpoint is parsed,
// disabled-policy entries are dropped, and supplemental metadata is mapped
// into ModelInfo. It also covers the 401-refresh path so token expiry mid
// listing does not surface to the operator.
func TestListUpstreamModels(t *testing.T) {
	var cpCalls int
	cpMux := http.NewServeMux()
	cpMux.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
		cpCalls++
		// Force a single 401 to exercise the invalidate+retry path.
		if cpCalls == 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, map[string]any{
			"data": []any{
				map[string]any{
					"id":     "gpt-4o",
					"name":   "GPT-4o",
					"vendor": "OpenAI",
					"capabilities": map[string]any{
						"type":     "chat",
						"family":   "gpt-4o",
						"supports": map[string]bool{"tool_calls": true, "streaming": true},
						"limits":   map[string]int{"max_prompt_tokens": 128000},
					},
					"policy": map[string]any{"state": "enabled"},
				},
				map[string]any{
					"id":     "blocked-model",
					"policy": map[string]any{"state": "disabled"},
				},
				map[string]any{
					"id":      "unpoliced",
					"vendor":  "Vendor",
					"preview": true,
				},
				map[string]any{
					// Missing id: must be ignored.
					"vendor": "ghost",
				},
			},
		})
	})
	cpSrv := httptest.NewServer(cpMux)
	defer cpSrv.Close()

	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/copilot_internal/v2/token", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"token":      "cop_xyz",
			"expires_at": time.Now().Add(time.Hour).Unix(),
		})
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

	models, err := p.ListUpstreamModels(context.Background())
	if err != nil {
		t.Fatalf("ListUpstreamModels: %v", err)
	}
	if cpCalls != 2 {
		t.Errorf("expected exactly one retry, got %d total calls", cpCalls)
	}

	got := make([]string, 0, len(models))
	for _, m := range models {
		got = append(got, m.ID)
	}
	want := []string{"gpt-4o", "unpoliced"}
	if !equalSlices(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}

	// Verify metadata mapping for the policy-enabled model.
	gpt := findModel(t, models, "gpt-4o")
	if gpt.Name != "GPT-4o" || gpt.Vendor != "OpenAI" {
		t.Errorf("name/vendor = %q/%q", gpt.Name, gpt.Vendor)
	}
	caps := strings.Join(gpt.Capabilities, ",")
	if !strings.Contains(caps, "chat") || !strings.Contains(caps, "tool_calls") {
		t.Errorf("capabilities = %v", gpt.Capabilities)
	}
	if gpt.Extras["family"] != "gpt-4o" {
		t.Errorf("family extra = %q", gpt.Extras["family"])
	}
	if gpt.Extras["limit_max_prompt_tokens"] != "128000" {
		t.Errorf("limit_max_prompt_tokens = %q", gpt.Extras["limit_max_prompt_tokens"])
	}
	if gpt.Extras["policy_state"] != "enabled" {
		t.Errorf("policy_state extra = %q", gpt.Extras["policy_state"])
	}
}

func TestListUpstreamModels_HTTPError(t *testing.T) {
	cpMux := http.NewServeMux()
	cpMux.HandleFunc("/models", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	cpSrv := httptest.NewServer(cpMux)
	defer cpSrv.Close()

	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/copilot_internal/v2/token", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"token": "x", "expires_at": time.Now().Add(time.Hour).Unix()})
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
	if _, err := p.ListUpstreamModels(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("expected HTTP 500 wrapped error, got %v", err)
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func findModel(t *testing.T, models []backend.ModelInfo, id string) backend.ModelInfo {
	t.Helper()
	for _, m := range models {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("model %q not found in %v", id, models)
	return backend.ModelInfo{}
}
