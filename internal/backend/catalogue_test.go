package backend_test

import (
	"context"
	"errors"
	"testing"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
)

// stubLister is a test-double for backend.ModelLister.
type stubLister struct {
	models []backend.ModelInfo
	err    error
}

func (s *stubLister) ListUpstreamModels(_ context.Context) ([]backend.ModelInfo, error) {
	return s.models, s.err
}

var upstreamModels = []backend.ModelInfo{
	{ID: "alpha", Vendor: "acme", Capabilities: []string{"chat"}},
	{ID: "beta", Vendor: "acme", Capabilities: []string{"chat", "embeddings"}},
	{ID: "gamma", Vendor: "acme"},
}

func TestFilter_EmptyAllowList(t *testing.T) {
	// Empty allow-list → nil, nil regardless of lister behaviour.
	lister := &stubLister{models: upstreamModels}
	got, err := backend.Filter(context.Background(), nil, lister)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil slice, got %v", got)
	}
}

func TestFilter_EmptyAllowList_ListerNotCalled(t *testing.T) {
	// Lister must not be called when allow-list is empty.
	var called bool
	lister := &stubLister{}
	lister.err = errors.New("should not be called")
	// Override so we can detect any call.
	spy := &spyLister{inner: lister, called: &called}
	_, _ = backend.Filter(context.Background(), nil, spy)
	if called {
		t.Error("lister was called despite empty allow-list")
	}
}

type spyLister struct {
	inner  backend.ModelLister
	called *bool
}

func (s *spyLister) ListUpstreamModels(ctx context.Context) ([]backend.ModelInfo, error) {
	*s.called = true
	return s.inner.ListUpstreamModels(ctx)
}

func TestFilter_AllowListFiltersUpstream(t *testing.T) {
	lister := &stubLister{models: upstreamModels}
	got, err := backend.Filter(context.Background(), []string{"alpha", "gamma"}, lister)
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

func TestFilter_AllowListModelNotInUpstream(t *testing.T) {
	// Model in allow-list but absent from upstream → silently omitted.
	lister := &stubLister{models: upstreamModels}
	got, err := backend.Filter(context.Background(), []string{"alpha", "missing"}, lister)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].ID != "alpha" {
		t.Errorf("want [alpha], got %v", got)
	}
}

func TestFilter_UpstreamError_Propagated(t *testing.T) {
	sentinel := errors.New("upstream unavailable")
	lister := &stubLister{err: sentinel}
	_, err := backend.Filter(context.Background(), []string{"alpha"}, lister)
	if !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel error, got %v", err)
	}
}

func TestFilter_PreservesFullModelInfo(t *testing.T) {
	// Filtered models retain all metadata fields, not just ID.
	lister := &stubLister{models: upstreamModels}
	got, err := backend.Filter(context.Background(), []string{"beta"}, lister)
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

func TestStubModels_Basic(t *testing.T) {
	got := backend.StubModels([]string{"x", "y"}, "myprovider")
	if len(got) != 2 {
		t.Fatalf("want 2 stubs, got %d", len(got))
	}
	for _, m := range got {
		if m.Vendor != "myprovider" {
			t.Errorf("Vendor = %q, want %q", m.Vendor, "myprovider")
		}
	}
	if got[0].ID != "x" || got[1].ID != "y" {
		t.Errorf("IDs = %v/%v", got[0].ID, got[1].ID)
	}
}

func TestStubModels_EmptyAllowList(t *testing.T) {
	got := backend.StubModels(nil, "v")
	if len(got) != 0 {
		t.Errorf("want empty slice, got %v", got)
	}
}
