package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"testing"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
	"github.com/thelonelyghost/llm-proxy/internal/server"
)

// budgetFakeBackend extends fakeBackend with a scripted Budget() method so
// we can also exercise the BudgetReporter capability.
type budgetFakeBackend struct {
	*fakeBackend
	budget    backend.Budget
	budgetErr error
}

func (b *budgetFakeBackend) Budget(_ context.Context) (backend.Budget, error) {
	if b.budgetErr != nil {
		return backend.Budget{}, b.budgetErr
	}
	return b.budget, nil
}

func newBudgetBackend(name string, bud backend.Budget) *budgetFakeBackend {
	return &budgetFakeBackend{fakeBackend: newFakeBackend(name, nil), budget: bud}
}

// startServerN runs the proxy with an arbitrary set of registered backends.
func startServerN(t *testing.T, backends ...backend.Backend) string {
	t.Helper()
	reg := backend.NewRegistry()
	for _, b := range backends {
		if err := reg.Register(b); err != nil {
			t.Fatal(err)
		}
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

// httpGet wraps http.Get with the boilerplate every test repeats (fail on
// transport error, fail on nil response, register the body for cleanup) so
// nilaway can prove the returned pointer is non-nil for the rest of the
// test.
func httpGet(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec // test URLs are constructed in-process
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestBudget_SingleBackend_Aggregate(t *testing.T) {
	b := newBudgetBackend("ibmbob", backend.Budget{
		Currency:  "bobcoins",
		MaxBudget: 100,
		Spend:     12.5,
		Remaining: 87.5,
		Extras:    map[string]string{"team_id": "team-1"},
	})
	base := startServerN(t, b)

	resp := httpGet(t, base+"/v1/usage/budget") //nolint:bodyclose // closed via t.Cleanup in httpGet
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out struct {
		Object string `json:"object"`
		Data   []struct {
			Object    string            `json:"object"`
			Backend   string            `json:"backend"`
			Currency  string            `json:"currency"`
			MaxBudget float64           `json:"max_budget"`
			Spend     float64           `json:"spend"`
			Remaining float64           `json:"remaining"`
			Unlimited bool              `json:"unlimited"`
			Extras    map[string]string `json:"extras"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Object != "list" || len(out.Data) != 1 {
		t.Fatalf("envelope = %+v", out)
	}
	got := out.Data[0]
	if got.Object != "usage.budget" || got.Backend != "ibmbob" ||
		got.MaxBudget != 100 || got.Spend != 12.5 || got.Remaining != 87.5 ||
		got.Currency != "bobcoins" || got.Extras["team_id"] != "team-1" {
		t.Errorf("item = %+v", got)
	}
}

func TestBudget_QueryFilter_SingleItem(t *testing.T) {
	b := newBudgetBackend("ibmbob", backend.Budget{
		Currency: "bobcoins", MaxBudget: 10, Spend: 3, Remaining: 7,
	})
	plain := newFakeBackend("copilot", []string{"gpt-4o"}) // no Budget()
	base := startServerN(t, b, plain)

	resp := httpGet(t, base+"/v1/usage/budget?backend=ibmbob") //nolint:bodyclose // closed via t.Cleanup in httpGet
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var item struct {
		Object    string  `json:"object"`
		Backend   string  `json:"backend"`
		MaxBudget float64 `json:"max_budget"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&item); err != nil {
		t.Fatal(err)
	}
	if item.Object != "usage.budget" || item.Backend != "ibmbob" || item.MaxBudget != 10 {
		t.Errorf("item = %+v", item)
	}
}

func TestBudget_UnknownBackend_404(t *testing.T) {
	b := newBudgetBackend("ibmbob", backend.Budget{Currency: "bobcoins"})
	base := startServerN(t, b)

	resp := httpGet(t, base+"/v1/usage/budget?backend=missing") //nolint:bodyclose // closed via t.Cleanup in httpGet
	if resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestBudget_BackendDoesNotReport_501(t *testing.T) {
	plain := newFakeBackend("copilot", []string{"gpt-4o"})
	base := startServerN(t, plain)

	t.Run("named", func(t *testing.T) {
		resp := httpGet(t, base+"/v1/usage/budget?backend=copilot") //nolint:bodyclose // closed via t.Cleanup in httpGet
		if resp.StatusCode != 501 {
			t.Errorf("status = %d, want 501", resp.StatusCode)
		}
	})

	t.Run("aggregate", func(t *testing.T) {
		resp := httpGet(t, base+"/v1/usage/budget") //nolint:bodyclose // closed via t.Cleanup in httpGet
		if resp.StatusCode != 501 {
			t.Errorf("status = %d, want 501", resp.StatusCode)
		}
	})
}

func TestBudget_ErrUnsupportedFromReporter_501(t *testing.T) {
	b := newBudgetBackend("ibmbob", backend.Budget{})
	// Use errors.Is-friendly wrapping so the handler classifies the error
	// as ErrUnsupported rather than a generic upstream failure.
	b.budgetErr = errUnsupportedWrap()
	base := startServerN(t, b)

	resp := httpGet(t, base+"/v1/usage/budget?backend=ibmbob") //nolint:bodyclose // closed via t.Cleanup in httpGet
	if resp.StatusCode != 501 {
		t.Errorf("status = %d, want 501", resp.StatusCode)
	}
}

func TestBudget_UpstreamFailure_502(t *testing.T) {
	b := newBudgetBackend("ibmbob", backend.Budget{})
	b.budgetErr = errors.New("upstream HTTP 503")
	base := startServerN(t, b)

	resp := httpGet(t, base+"/v1/usage/budget?backend=ibmbob") //nolint:bodyclose // closed via t.Cleanup in httpGet
	if resp.StatusCode != 502 {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

// errUnsupportedWrap returns an error that wraps backend.ErrUnsupported so
// errors.Is succeeds, mirroring how providers return it in production.
func errUnsupportedWrap() error {
	return wrappedErr{inner: backend.ErrUnsupported}
}

type wrappedErr struct{ inner error }

func (w wrappedErr) Error() string { return "wrap: " + w.inner.Error() }
func (w wrappedErr) Unwrap() error { return w.inner }
