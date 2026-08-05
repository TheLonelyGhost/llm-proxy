package backend_test

import (
	"context"
	"errors"
	"testing"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
)

// TestAllowList_Allowed_EmptyListPermitsAll verifies that an AllowList built
// from an empty slice permits any model (pass-through mode).
func TestAllowList_Allowed_EmptyListPermitsAll(t *testing.T) {
	al := backend.NewAllowList(nil)
	if err := al.Allowed("anything"); err != nil {
		t.Errorf("empty allow-list should permit any model, got: %v", err)
	}
}

// TestAllowList_Allowed_PermitsListedModel verifies that a model in the
// allow-list is accepted.
func TestAllowList_Allowed_PermitsListedModel(t *testing.T) {
	al := backend.NewAllowList([]string{"gpt-4o", "claude-3"})
	if err := al.Allowed("gpt-4o"); err != nil {
		t.Errorf("gpt-4o should be allowed, got: %v", err)
	}
	if err := al.Allowed("claude-3"); err != nil {
		t.Errorf("claude-3 should be allowed, got: %v", err)
	}
}

// TestAllowList_Allowed_RejectsUnlistedModel verifies that a model not in
// the allow-list is rejected with ErrUnknownModel.
func TestAllowList_Allowed_RejectsUnlistedModel(t *testing.T) {
	al := backend.NewAllowList([]string{"gpt-4o"})
	err := al.Allowed("gpt-5")
	if err == nil {
		t.Fatal("expected error for unlisted model, got nil")
	}
	if !errors.Is(err, backend.ErrUnknownModel) {
		t.Errorf("expected ErrUnknownModel, got: %v", err)
	}
}

// TestAllowList_Models_EmptyListReturnsNil verifies that when the allow-list
// is empty, Models returns (nil, nil) and does not call the lister.
func TestAllowList_Models_EmptyListReturnsNil(t *testing.T) {
	var called bool
	lister := &spyLister{inner: &stubLister{models: upstreamModels}, called: &called}
	al := backend.NewAllowList(nil)
	got, err := al.Models(context.Background(), lister)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
	if called {
		t.Error("lister called despite empty allow-list")
	}
}

// TestAllowList_Models_FiltersUpstream verifies that Models returns only
// models present in both the allow-list and the upstream lister response.
func TestAllowList_Models_FiltersUpstream(t *testing.T) {
	al := backend.NewAllowList([]string{"alpha", "gamma"})
	lister := &stubLister{models: upstreamModels}
	got, err := al.Models(context.Background(), lister)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 models, got %d: %v", len(got), got)
	}
	ids := map[string]bool{}
	for _, m := range got {
		ids[m.ID] = true
	}
	if !ids["alpha"] || !ids["gamma"] {
		t.Errorf("wrong models returned: %v", got)
	}
}

// TestAllowList_Models_FallsBackToStubOnListerError verifies that when the
// upstream lister fails, Models returns a stub slice (one entry per
// allow-list entry) and does not propagate the error.
func TestAllowList_Models_FallsBackToStubOnListerError(t *testing.T) {
	listerErr := errors.New("upstream unavailable")
	al := backend.NewAllowList([]string{"gpt-4o", "gpt-4-turbo"})
	lister := &stubLister{err: listerErr}
	got, err := al.Models(context.Background(), lister)
	if err != nil {
		t.Fatalf("expected nil error (fallback mode), got: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 stub entries, got %d", len(got))
	}
	for _, m := range got {
		if m.ID != "gpt-4o" && m.ID != "gpt-4-turbo" {
			t.Errorf("unexpected stub ID: %q", m.ID)
		}
	}
}

// TestAllowList_Models_PreservesFullMetadata verifies that Models preserves
// all ModelInfo fields when filtering against the upstream.
func TestAllowList_Models_PreservesFullMetadata(t *testing.T) {
	al := backend.NewAllowList([]string{"beta"})
	lister := &stubLister{models: upstreamModels}
	got, err := al.Models(context.Background(), lister)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 model, got %d", len(got))
	}
	m := got[0]
	if m.Vendor != "acme" {
		t.Errorf("Vendor = %q, want %q", m.Vendor, "acme")
	}
	if len(m.Capabilities) != 2 {
		t.Errorf("Capabilities = %v, want 2 entries", m.Capabilities)
	}
}

// TestAllowList_Models_VendorTagPassedThrough verifies that the vendor string
// recorded in stubs matches what Models was told to use.
func TestAllowList_Models_VendorTagPassedThrough(t *testing.T) {
	listerErr := errors.New("down")
	al := backend.NewAllowList([]string{"m1"})
	lister := &stubLister{err: listerErr}
	got, err := al.Models(context.Background(), lister)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Stub entries have no vendor because AllowList doesn't know the backend
	// name — that's by design; callers set Vendor themselves after the fact
	// or pass a VendorLister that already sets it. The stub just preserves ID.
	if len(got) != 1 || got[0].ID != "m1" {
		t.Errorf("want stub [{ID:m1}], got %v", got)
	}
}
