package cli_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// TestProvidersListModels_DefaultConfigMissing ensures the command now falls
// back to the platform default when no config source is specified.
func TestProvidersListModels_DefaultConfigMissing(t *testing.T) {
	t.Setenv("LLM_PROXY_CONFIG", filepath.Join(t.TempDir(), "missing.hcl"))
	_, err := run(t, "providers", "list-models", "ibm-bob")
	if err == nil {
		t.Fatal("expected error for missing default config")
	}
}

// TestProvidersListModels_UnknownBackend covers the case where the user
// passes a label that does not appear in the loaded HCL config.
func TestProvidersListModels_UnknownBackend(t *testing.T) {
	cfg := writeConfig(t, `
backend "bob" {
  type    = "ibm-bob"
  api_key = "sk-x"
}
`)
	_, err := run(t, "providers", "list-models", "ghost", "-c", cfg)
	if err == nil || !strings.Contains(err.Error(), `no backend "ghost"`) {
		t.Fatalf("err = %v", err)
	}
}

// TestProvidersListModels_PlainAndVerbose exercises the happy path against a
// fake IBM Bob upstream and verifies both the default (id-per-line) and
// --verbose renderings.
func TestProvidersListModels_PlainAndVerbose(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/model/info", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"alpha","owned_by":"ibm","description":"first"},
			{"id":"beta","owned_by":"ibm"}
		]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := writeConfig(t, fmt.Sprintf(`
backend "bob" {
  type     = "ibm-bob"
  api_key  = "sk-test"
  api_base = "%s"
}
`, srv.URL))

	plain, err := run(t, "providers", "list-models", "bob", "-c", cfg)
	if err != nil {
		t.Fatalf("plain: %v\n%s", err, plain)
	}
	gotLines := strings.Split(strings.TrimSpace(plain), "\n")
	if len(gotLines) != 2 || gotLines[0] != "alpha" || gotLines[1] != "beta" {
		t.Errorf("plain output = %q", plain)
	}

	verbose, err := run(t, "providers", "list-models", "bob", "-c", cfg, "--verbose")
	if err != nil {
		t.Fatalf("verbose: %v\n%s", err, verbose)
	}
	if !strings.Contains(verbose, "alpha") || !strings.Contains(verbose, "description: first") {
		t.Errorf("verbose missing description:\n%s", verbose)
	}
	if !strings.Contains(verbose, "vendor: ibm") {
		t.Errorf("verbose missing vendor:\n%s", verbose)
	}

	jsonOut, err := run(t, "providers", "list-models", "bob", "-c", cfg, "--json")
	if err != nil {
		t.Fatalf("json: %v\n%s", err, jsonOut)
	}
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("decode json: %v\n%s", err, jsonOut)
	}
	if len(decoded) != 2 || decoded[0]["id"] != "alpha" {
		t.Errorf("json payload = %v", decoded)
	}
}

// TestProvidersListModels_UnsupportedBackend confirms a clear error when the
// resolved backend does not implement the optional ModelLister interface.
//
// We can't easily register a third backend type from a test, but every
// shipped backend currently implements ModelLister. To still exercise the
// branch, point at a backend whose upstream errors out and ensure the error
// surfaces sensibly. (A future provider lacking the capability is covered
// by the type assertion in the command implementation.)
func TestProvidersListModels_UpstreamError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/model/info", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "denied", http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := writeConfig(t, fmt.Sprintf(`
backend "bob" {
  type     = "ibm-bob"
  api_key  = "sk-test"
  api_base = "%s"
}
`, srv.URL))
	_, err := run(t, "providers", "list-models", "bob", "-c", cfg)
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("err = %v", err)
	}
}
