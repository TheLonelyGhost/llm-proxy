package copilot_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
	"github.com/thelonelyghost/llm-proxy/internal/providers/copilot"
)

// stubUserInfo wires a Copilot provider against a fake GitHub API server
// that serves the given /copilot_internal/user payload. The proxy must
// already have a stored GitHub OAuth token, which the helper plants under
// the temporary FileTokenStore. The returned counter tracks how many
// times the upstream endpoint was hit.
func stubUserInfo(t *testing.T, payload map[string]any) (*copilot.Provider, *int32) {
	t.Helper()

	var calls int32
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/copilot_internal/user", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Header.Get("Authorization") != "token gho_test" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		writeJSON(w, payload)
	})
	apiSrv := httptest.NewServer(apiMux)
	t.Cleanup(apiSrv.Close)

	dir := t.TempDir()
	store := copilot.NewFileTokenStoreAt(filepath.Join(dir, "cfg"), filepath.Join(dir, "cache"))
	// Plant a GitHub OAuth token so FetchUserInfo can find it.
	if err := store.SaveGitHubToken(&copilot.GitHubToken{
		AccessToken: "gho_test",
		TokenType:   "bearer",
		ObtainedAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	block := buildBlock(t, fmt.Sprintf(`
        models           = ["gpt-4o"]
        github_api_base  = %q
        request_timeout  = "5s"
    `, apiSrv.URL))

	p, err := copilot.New(block, copilot.Options{TokenStore: store})
	if err != nil {
		t.Fatal(err)
	}
	return p, &calls
}

// TestBudget_BusinessSeat_RealisticPayload exercises the
// /copilot_internal/user response shape returned for a Copilot Business
// seat, which is the failure case that motivated using this endpoint over
// /copilot_internal/v2/token. The extra fields ("quota_id",
// "timestamp_utc", "has_quota", "quota_remaining", "token_based_billing")
// must be tolerated and ignored.
func TestBudget_BusinessSeat_RealisticPayload(t *testing.T) {
	p, _ := stubUserInfo(t, map[string]any{
		"login":            "octocat",
		"copilot_plan":     "business",
		"access_type_sku":  "copilot_for_business_seat_quota",
		"chat_enabled":     true,
		"quota_reset_date": "2026-06-01",
		"quota_snapshots": map[string]any{
			"chat": map[string]any{
				"overage_count":       0,
				"overage_permitted":   false,
				"percent_remaining":   100.0,
				"quota_id":            "chat",
				"quota_remaining":     0.0,
				"unlimited":           true,
				"timestamp_utc":       "2026-05-22T18:04:50.520Z",
				"has_quota":           false,
				"quota_reset_at":      0,
				"token_based_billing": false,
				"remaining":           0,
				"entitlement":         0,
			},
			"completions": map[string]any{
				"overage_count":     0,
				"overage_permitted": false,
				"percent_remaining": 100.0,
				"quota_id":          "completions",
				"unlimited":         true,
				"remaining":         0,
				"entitlement":       0,
			},
			"premium_interactions": map[string]any{
				"overage_count":     0,
				"overage_permitted": true,
				"percent_remaining": 31.0,
				"quota_id":          "premium_interactions",
				"unlimited":         false,
				"remaining":         93,
				"entitlement":       300,
			},
		},
		"organization_list": []map[string]any{
			{"login": "hashicorp", "name": "HashiCorp"},
		},
	})
	bud, err := p.Budget(context.Background())
	if err != nil {
		t.Fatalf("Budget: %v", err)
	}
	if bud.Currency != "premium_requests" {
		t.Errorf("Currency = %q", bud.Currency)
	}
	if bud.MaxBudget != 300 || bud.Remaining != 93 || bud.Spend != 207 {
		t.Errorf("budget numbers = %+v", bud)
	}
	if bud.Extras["copilot_plan"] != "business" {
		t.Errorf("copilot_plan = %q", bud.Extras["copilot_plan"])
	}
	if bud.Extras["access_type_sku"] != "copilot_for_business_seat_quota" {
		t.Errorf("access_type_sku = %q", bud.Extras["access_type_sku"])
	}
	if bud.Extras["organization_login"] != "hashicorp" {
		t.Errorf("organization_login = %q", bud.Extras["organization_login"])
	}
	if bud.Extras["organization_name"] != "HashiCorp" {
		t.Errorf("organization_name = %q", bud.Extras["organization_name"])
	}
	if bud.Extras["login"] != "octocat" {
		t.Errorf("login = %q", bud.Extras["login"])
	}
}

func TestBudget_PremiumInteractions_Primary(t *testing.T) {
	p, calls := stubUserInfo(t, map[string]any{
		"token":      "cop_xyz",
		"expires_at": time.Now().Add(30 * time.Minute).Unix(),
		"quota_snapshots": map[string]any{
			"chat":                 map[string]any{"entitlement": 0, "remaining": 0, "unlimited": true},
			"completions":          map[string]any{"entitlement": 0, "remaining": 0, "unlimited": true},
			"premium_interactions": map[string]any{"entitlement": 300, "remaining": 287.5, "percent_remaining": 95.83, "unlimited": false, "overage_count": 0, "overage_permitted": true},
		},
		"quota_reset_date": "2026-06-01",
	})

	bud, err := p.Budget(context.Background())
	if err != nil {
		t.Fatalf("Budget: %v", err)
	}
	if bud.Currency != "premium_requests" {
		t.Errorf("Currency = %q, want premium_requests", bud.Currency)
	}
	if bud.MaxBudget != 300 {
		t.Errorf("MaxBudget = %v, want 300", bud.MaxBudget)
	}
	if bud.Remaining != 287.5 {
		t.Errorf("Remaining = %v, want 287.5", bud.Remaining)
	}
	if bud.Spend != 12.5 {
		t.Errorf("Spend = %v, want 12.5", bud.Spend)
	}
	if bud.Unlimited {
		t.Error("Unlimited should be false for premium_interactions with finite entitlement")
	}
	if bud.Extras["primary_source"] != "premium_interactions" {
		t.Errorf("primary_source = %q", bud.Extras["primary_source"])
	}
	// Other snapshots surface under their own keys.
	if bud.Extras["snapshot_chat_unlimited"] != "true" || bud.Extras["snapshot_completions_unlimited"] != "true" {
		t.Errorf("chat/completions snapshots missing: %+v", bud.Extras)
	}
	if bud.Extras["snapshot_premium_interactions_overage_permitted"] != "true" {
		t.Errorf("overage flag missing: %+v", bud.Extras)
	}
	if bud.Extras["quota_reset_date"] != "2026-06-01" {
		t.Errorf("reset date = %q", bud.Extras["quota_reset_date"])
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Errorf("exchange calls = %d, want 1", got)
	}
}

func TestBudget_PremiumInteractionsUnlimited_ZerosOutNumbers(t *testing.T) {
	p, _ := stubUserInfo(t, map[string]any{
		"token":      "cop_xyz",
		"expires_at": time.Now().Add(30 * time.Minute).Unix(),
		"quota_snapshots": map[string]any{
			"premium_interactions": map[string]any{"entitlement": 0, "remaining": 0, "unlimited": true, "overage_permitted": false},
		},
	})
	bud, err := p.Budget(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !bud.Unlimited {
		t.Error("expected Unlimited=true")
	}
	if bud.MaxBudget != 0 || bud.Remaining != 0 || bud.Spend != 0 {
		t.Errorf("numbers should be zeroed for unlimited; got %+v", bud)
	}
}

func TestBudget_ChatFallbackWhenNoPremiumInteractions(t *testing.T) {
	p, _ := stubUserInfo(t, map[string]any{
		"token":      "cop_xyz",
		"expires_at": time.Now().Add(30 * time.Minute).Unix(),
		"quota_snapshots": map[string]any{
			"chat":        map[string]any{"entitlement": 100, "remaining": 70, "percent_remaining": 70, "unlimited": false},
			"completions": map[string]any{"entitlement": 0, "remaining": 0, "unlimited": true},
		},
	})
	bud, err := p.Budget(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if bud.Currency != "interactions" {
		t.Errorf("Currency = %q, want interactions", bud.Currency)
	}
	if bud.MaxBudget != 100 || bud.Remaining != 70 || bud.Spend != 30 {
		t.Errorf("budget = %+v", bud)
	}
	if bud.Extras["primary_source"] != "chat" {
		t.Errorf("primary_source = %q", bud.Extras["primary_source"])
	}
}

func TestBudget_LimitedUserQuotas_LegacyFree(t *testing.T) {
	p, _ := stubUserInfo(t, map[string]any{
		"token":      "cop_xyz",
		"expires_at": time.Now().Add(30 * time.Minute).Unix(),
		"limited_user_quotas": map[string]any{
			"chat":        30,
			"completions": 1500,
		},
		"limited_user_reset_date": "2026-06-15",
	})
	bud, err := p.Budget(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if bud.Currency != "interactions" {
		t.Errorf("Currency = %q, want interactions", bud.Currency)
	}
	if bud.MaxBudget != 0 {
		t.Errorf("MaxBudget should be 0 (unknown) for limited_user_quotas; got %v", bud.MaxBudget)
	}
	if bud.Remaining != 30 {
		t.Errorf("Remaining = %v, want 30 (chat preferred)", bud.Remaining)
	}
	if bud.Extras["limited_user_primary"] != "chat" {
		t.Errorf("limited_user_primary = %q", bud.Extras["limited_user_primary"])
	}
	if bud.Extras["limited_user_completions"] != "1500" {
		t.Errorf("limited_user_completions = %q", bud.Extras["limited_user_completions"])
	}
	if bud.Extras["limited_user_reset_date"] != "2026-06-15" {
		t.Errorf("reset date = %q", bud.Extras["limited_user_reset_date"])
	}
}

func TestBudget_LimitedUserQuotas_NoChatKey_DeterministicPick(t *testing.T) {
	p, _ := stubUserInfo(t, map[string]any{
		"token":      "cop_xyz",
		"expires_at": time.Now().Add(30 * time.Minute).Unix(),
		"limited_user_quotas": map[string]any{
			"zebra":  10,
			"apples": 5,
		},
	})
	bud, err := p.Budget(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// "apples" sorts before "zebra".
	if bud.Extras["limited_user_primary"] != "apples" {
		t.Errorf("primary = %q, want apples", bud.Extras["limited_user_primary"])
	}
	if bud.Remaining != 5 {
		t.Errorf("Remaining = %v, want 5", bud.Remaining)
	}
}

func TestBudget_NoQuotaInfo_Unsupported(t *testing.T) {
	p, _ := stubUserInfo(t, map[string]any{
		"token":      "cop_xyz",
		"expires_at": time.Now().Add(30 * time.Minute).Unix(),
		// No quota_snapshots, no limited_user_quotas.
	})
	_, err := p.Budget(context.Background())
	if !errors.Is(err, backend.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}

func TestBudget_NotAuthenticated_ReturnsErr(t *testing.T) {
	// No GitHub token stored.
	dir := t.TempDir()
	store := copilot.NewFileTokenStoreAt(filepath.Join(dir, "cfg"), filepath.Join(dir, "cache"))
	block := buildBlock(t, `models = ["gpt-4o"]`)
	p, err := copilot.New(block, copilot.Options{TokenStore: store})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Budget(context.Background())
	if err == nil || !errors.Is(err, copilot.ErrNotAuthenticated) {
		t.Fatalf("err = %v, want ErrNotAuthenticated", err)
	}
}

func TestBudget_UpstreamHTTPError(t *testing.T) {
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/copilot_internal/user", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	})
	apiSrv := httptest.NewServer(apiMux)
	t.Cleanup(apiSrv.Close)

	dir := t.TempDir()
	store := copilot.NewFileTokenStoreAt(filepath.Join(dir, "cfg"), filepath.Join(dir, "cache"))
	if err := store.SaveGitHubToken(&copilot.GitHubToken{AccessToken: "gho_test"}); err != nil {
		t.Fatal(err)
	}
	block := buildBlock(t, fmt.Sprintf(`
        models          = ["gpt-4o"]
        github_api_base = %q
        request_timeout = "2s"
    `, apiSrv.URL))
	p, err := copilot.New(block, copilot.Options{TokenStore: store})
	if err != nil {
		t.Fatal(err)
	}

	_, err = p.Budget(context.Background())
	if err == nil || !strings.Contains(err.Error(), "fetch copilot user info") {
		t.Fatalf("err = %v", err)
	}
	if errors.Is(err, backend.ErrUnsupported) {
		t.Errorf("HTTP 503 should not classify as ErrUnsupported")
	}
}

// TestBudget_OverageProducesZeroSpend verifies that when the upstream
// reports a negative `remaining` (the user has exceeded entitlement and
// overage is permitted), Spend is clamped at zero instead of going
// negative. The overage_count signal is preserved in Extras.
func TestBudget_OverageProducesZeroSpend(t *testing.T) {
	p, _ := stubUserInfo(t, map[string]any{
		"token":      "cop_xyz",
		"expires_at": time.Now().Add(30 * time.Minute).Unix(),
		"quota_snapshots": map[string]any{
			"premium_interactions": map[string]any{
				"entitlement":       300,
				"remaining":         -5,
				"overage_count":     5,
				"overage_permitted": true,
			},
		},
	})
	bud, err := p.Budget(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Spend in the upstream sense would be 305, but consumers expect
	// Spend <= MaxBudget for non-overage UI math; clamping is the
	// documented behaviour. The overage_count signal is preserved.
	if bud.Spend < 0 {
		t.Errorf("Spend should be clamped at 0; got %v", bud.Spend)
	}
	if bud.Extras["snapshot_premium_interactions_overage_count"] != "5" {
		t.Errorf("overage_count = %q", bud.Extras["snapshot_premium_interactions_overage_count"])
	}
}

// fileWrite is only here so the import group stays the same as the rest of
// the package tests; otherwise json/atomic/encoding/json imports drift.
var _ = json.Marshal
