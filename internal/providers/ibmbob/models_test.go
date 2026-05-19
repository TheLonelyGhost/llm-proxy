package ibmbob_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thelonelyghost/llm-proxy/internal/providers/ibmbob"
)

// TestListUpstreamModels_AuthnBackend exercises the
// /inference/v1/model/info route for the modern backend, including header
// signing, identity resolution, and supplemental-field extraction (both
// top-level and nested `model_info` keys) into Extras.
func TestListUpstreamModels_AuthnBackend(t *testing.T) {
	var modelCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/v1/profile", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"instances":[{"instance_id":"i","teams":[{"id":"t"}]}]}`)
	})
	mux.HandleFunc("/inference/v1/model/info", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&modelCalls, 1)
		if r.Header.Get("x-instance-id") != "i" || r.Header.Get("x-team-id") != "t" {
			t.Errorf("identity headers missing: %v", r.Header)
		}
		if r.Header.Get("x-request-signature") == "" {
			t.Errorf("expected HMAC signature")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[
			{"model_name":"premium","owned_by":"ibm","description":"big model","model_info":{"max_tokens":8192,"supports_function_calling":true}},
			{"model_name":"basic","owned_by":"ibm"},
			{"object":"model"}
		]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	block := buildBlock(t, fmt.Sprintf(`
        api_key     = "%s"
        hmac_secret = "s"
        api_base    = "%s"
    `, makeJWTKey(), srv.URL))
	p, err := ibmbob.New(block, ibmbob.Options{Now: fixedNow()})
	if err != nil {
		t.Fatal(err)
	}
	models, err := p.ListUpstreamModels(context.Background())
	if err != nil {
		t.Fatalf("ListUpstreamModels: %v", err)
	}
	if atomic.LoadInt32(&modelCalls) != 1 {
		t.Errorf("expected 1 models call, got %d", modelCalls)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v (want 2 after filtering entry with no id)", models)
	}
	if models[0].ID != "basic" || models[1].ID != "premium" {
		t.Errorf("sort order = %v", []string{models[0].ID, models[1].ID})
	}
	premium := models[1]
	if premium.Vendor != "ibm" {
		t.Errorf("vendor = %q", premium.Vendor)
	}
	if premium.Extras["description"] != "big model" {
		t.Errorf("description extra = %q", premium.Extras["description"])
	}
	if premium.Extras["info_max_tokens"] != "8192" {
		t.Errorf("info_max_tokens extra = %q", premium.Extras["info_max_tokens"])
	}
	if premium.Extras["info_supports_function_calling"] != "true" {
		t.Errorf("info_supports_function_calling = %q", premium.Extras["info_supports_function_calling"])
	}
}

// TestListUpstreamModels_LegacyBackend covers the /v1/model/info route used
// by legacy `sk-`/`pk-` API keys, and confirms the profile fetch is
// skipped. It also asserts that entries using the plain OpenAI `id` field
// (instead of `model_name`) are still accepted, so the decoder remains
// resilient to upstream surface changes.
func TestListUpstreamModels_LegacyBackend(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/v1/profile", func(_ http.ResponseWriter, _ *http.Request) {
		t.Errorf("legacy backend should not call /admin/v1/profile")
	})
	mux.HandleFunc("/v1/model/info", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"legacy-model","owned_by":"ibm"}]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	block := buildBlock(t, fmt.Sprintf(`
        api_key  = "sk-test"
        api_base = "%s"
    `, srv.URL))
	p, err := ibmbob.New(block, ibmbob.Options{Now: fixedNow()})
	if err != nil {
		t.Fatal(err)
	}
	models, err := p.ListUpstreamModels(context.Background())
	if err != nil {
		t.Fatalf("ListUpstreamModels: %v", err)
	}
	if len(models) != 1 || models[0].ID != "legacy-model" {
		t.Fatalf("models = %+v", models)
	}
}

func TestListUpstreamModels_HTTPError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/model/info", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	block := buildBlock(t, fmt.Sprintf(`
        api_key  = "sk-test"
        api_base = "%s"
    `, srv.URL))
	p, err := ibmbob.New(block, ibmbob.Options{Now: fixedNow()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.ListUpstreamModels(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("expected HTTP 403 wrapped error, got %v", err)
	}
}
