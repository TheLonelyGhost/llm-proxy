package backend_test

import (
	"context"
	"errors"
	"testing"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
)

type stubBackend struct{ name string }

func (s stubBackend) Name() string { return s.name }
func (s stubBackend) Models(context.Context) ([]backend.Model, error) {
	return []backend.Model{{ID: "m", OwnedBy: s.name}}, nil
}
func (s stubBackend) Chat(context.Context, backend.ChatRequest) (*backend.Response, error) {
	return nil, backend.ErrUnsupported
}
func (s stubBackend) Complete(context.Context, backend.CompletionRequest) (*backend.Response, error) {
	return nil, backend.ErrUnsupported
}
func (s stubBackend) Embed(context.Context, backend.EmbeddingRequest) (*backend.Response, error) {
	return nil, backend.ErrUnsupported
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := backend.NewRegistry()
	if err := r.Register(stubBackend{name: "alpha"}); err != nil {
		t.Fatalf("register alpha: %v", err)
	}
	if err := r.Register(stubBackend{name: "beta"}); err != nil {
		t.Fatalf("register beta: %v", err)
	}
	b, ok := r.Get("alpha")
	if !ok || b == nil || b.Name() != "alpha" {
		t.Fatalf("Get(alpha) = %v %v", b, ok)
	}
	if _, ok := r.Get("nope"); ok {
		t.Fatalf("unexpected hit")
	}
	got := r.Names()
	if len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Errorf("Names() = %v", got)
	}
	all := r.All()
	if len(all) != 2 || all[0].Name() != "alpha" || all[1].Name() != "beta" {
		t.Errorf("All() = %v", all)
	}
}

func TestRegistry_Errors(t *testing.T) {
	r := backend.NewRegistry()
	cases := []struct {
		name string
		b    backend.Backend
	}{
		{"nil", nil},
		{"empty name", stubBackend{name: ""}},
		{"uppercase", stubBackend{name: "Mixed"}},
		{"with slash", stubBackend{name: "a/b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := r.Register(tc.b); err == nil {
				t.Fatal("expected error")
			}
		})
	}

	if err := r.Register(stubBackend{name: "dup"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(stubBackend{name: "dup"}); err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestSentinelErrors(t *testing.T) {
	if !errors.Is(backend.ErrUnsupported, backend.ErrUnsupported) {
		t.Fatal("ErrUnsupported identity broken")
	}
	if !errors.Is(backend.ErrUnknownModel, backend.ErrUnknownModel) {
		t.Fatal("ErrUnknownModel identity broken")
	}
}
