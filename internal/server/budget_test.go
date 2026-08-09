package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// toolRunnerBackend extends fakeBackend with a scripted RunTool() method so
// we can exercise the ToolRunner path without a real plugin binary.
type toolRunnerBackend struct {
	*fakeBackend
	// tools maps tool name -> (stdout bytes, error). A missing key means the
	// tool is not registered (RunTool returns backend.ErrToolNotFound).
	tools map[string]toolResult
}

type toolResult struct {
	out []byte
	err error
}

func newToolRunnerBackend(name string, tools map[string]toolResult) *toolRunnerBackend {
	return &toolRunnerBackend{
		fakeBackend: newFakeBackend(name, nil),
		tools:       tools,
	}
}

func (b *toolRunnerBackend) RunTool(_ context.Context, toolName string) ([]byte, error) {
	r, ok := b.tools[toolName]
	if !ok {
		return nil, fmt.Errorf("%w: %q not registered", backend.ErrToolNotFound, toolName)
	}
	return r.out, r.err
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

// ---- BudgetReporter tests (compiled-in interface, unchanged semantics) -----

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

// ---- ToolRunner tests (sidecar plugin budget tool path) --------------------

// validBudgetJSON returns a BudgetResponse payload matching budget.schema.json.
func validBudgetJSON(currency string, maxBudget, spend, remaining float64) []byte {
	return []byte(fmt.Sprintf(
		`{"object":"usage.budget","currency":%q,"max_budget":%g,"spend":%g,"remaining":%g}`,
		currency, maxBudget, spend, remaining,
	))
}

func TestBudget_ToolRunner_Aggregate(t *testing.T) {
	b := newToolRunnerBackend("myplugin", map[string]toolResult{
		"budget": {out: validBudgetJSON("usd", 50, 10, 40)},
	})
	base := startServerN(t, b)

	resp := httpGet(t, base+"/v1/usage/budget") //nolint:bodyclose
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Object string `json:"object"`
		Data   []struct {
			Object    string  `json:"object"`
			Backend   string  `json:"backend"`
			Currency  string  `json:"currency"`
			MaxBudget float64 `json:"max_budget"`
			Spend     float64 `json:"spend"`
			Remaining float64 `json:"remaining"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Object != "list" || len(out.Data) != 1 {
		t.Fatalf("envelope = %+v", out)
	}
	got := out.Data[0]
	if got.Object != "usage.budget" {
		t.Errorf("object = %q, want usage.budget", got.Object)
	}
	if got.Backend != "myplugin" {
		t.Errorf("backend = %q, want myplugin", got.Backend)
	}
	if got.Currency != "usd" || got.MaxBudget != 50 || got.Spend != 10 || got.Remaining != 40 {
		t.Errorf("values = %+v", got)
	}
}

func TestBudget_ToolRunner_SingleBackend(t *testing.T) {
	b := newToolRunnerBackend("myplugin", map[string]toolResult{
		"budget": {out: validBudgetJSON("bobcoins", 100, 25, 75)},
	})
	base := startServerN(t, b)

	resp := httpGet(t, base+"/v1/usage/budget?backend=myplugin") //nolint:bodyclose
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var item struct {
		Object    string  `json:"object"`
		Backend   string  `json:"backend"`
		MaxBudget float64 `json:"max_budget"`
		Remaining float64 `json:"remaining"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&item); err != nil {
		t.Fatal(err)
	}
	if item.Backend != "myplugin" || item.MaxBudget != 100 || item.Remaining != 75 {
		t.Errorf("item = %+v", item)
	}
}

func TestBudget_ToolRunner_KeyedByProviderLabel(t *testing.T) {
	// Two tool-runner backends; verify each item carries its own label.
	a := newToolRunnerBackend("alpha", map[string]toolResult{
		"budget": {out: validBudgetJSON("usd", 200, 50, 150)},
	})
	b := newToolRunnerBackend("beta", map[string]toolResult{
		"budget": {out: validBudgetJSON("eur", 300, 100, 200)},
	})
	base := startServerN(t, a, b)

	resp := httpGet(t, base+"/v1/usage/budget") //nolint:bodyclose
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Object string `json:"object"`
		Data   []struct {
			Backend string `json:"backend"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data) != 2 {
		t.Fatalf("expected 2 items, got %d", len(out.Data))
	}
	seen := map[string]bool{}
	for _, d := range out.Data {
		seen[d.Backend] = true
	}
	if !seen["alpha"] || !seen["beta"] {
		t.Errorf("expected both alpha and beta in response, got %v", seen)
	}
}

func TestBudget_ToolRunner_NoToolRegistered_501(t *testing.T) {
	// ToolRunner backend but no "budget" tool in manifest.
	b := newToolRunnerBackend("myplugin", map[string]toolResult{
		"login": {out: []byte(`{"ok":true}`)},
	})
	base := startServerN(t, b)

	t.Run("named", func(t *testing.T) {
		resp := httpGet(t, base+"/v1/usage/budget?backend=myplugin") //nolint:bodyclose
		if resp.StatusCode != 501 {
			t.Errorf("status = %d, want 501", resp.StatusCode)
		}
	})

	t.Run("aggregate", func(t *testing.T) {
		resp := httpGet(t, base+"/v1/usage/budget") //nolint:bodyclose
		if resp.StatusCode != 501 {
			t.Errorf("status = %d, want 501", resp.StatusCode)
		}
	})
}

func TestBudget_ToolRunner_ErrorOutput_502(t *testing.T) {
	// Tool exits non-zero and emits a BudgetErrorResponse (schema discriminator).
	errPayload := []byte(`{"object":"usage.budget","error":"upstream API unreachable"}`)
	b := newToolRunnerBackend("myplugin", map[string]toolResult{
		"budget": {out: errPayload, err: errors.New("exit status 1")},
	})
	base := startServerN(t, b)

	resp := httpGet(t, base+"/v1/usage/budget?backend=myplugin") //nolint:bodyclose
	if resp.StatusCode != 502 {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

func TestBudget_ToolRunner_InvalidJSON_502(t *testing.T) {
	b := newToolRunnerBackend("myplugin", map[string]toolResult{
		"budget": {out: []byte(`not valid json at all`)},
	})
	base := startServerN(t, b)

	resp := httpGet(t, base+"/v1/usage/budget?backend=myplugin") //nolint:bodyclose
	if resp.StatusCode != 502 {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

func TestBudget_ToolRunner_WrongObjectField_502(t *testing.T) {
	b := newToolRunnerBackend("myplugin", map[string]toolResult{
		"budget": {out: []byte(`{"object":"something.else","max_budget":10,"spend":1,"remaining":9}`)},
	})
	base := startServerN(t, b)

	resp := httpGet(t, base+"/v1/usage/budget?backend=myplugin") //nolint:bodyclose
	if resp.StatusCode != 502 {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

func TestBudget_ToolRunner_Unlimited(t *testing.T) {
	payload := []byte(`{"object":"usage.budget","unlimited":true,"max_budget":0,"spend":0,"remaining":0}`)
	b := newToolRunnerBackend("myplugin", map[string]toolResult{
		"budget": {out: payload},
	})
	base := startServerN(t, b)

	resp := httpGet(t, base+"/v1/usage/budget?backend=myplugin") //nolint:bodyclose
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var item struct {
		Unlimited bool    `json:"unlimited"`
		MaxBudget float64 `json:"max_budget"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&item); err != nil {
		t.Fatal(err)
	}
	if !item.Unlimited {
		t.Error("expected unlimited=true")
	}
	if item.MaxBudget != 0 {
		t.Errorf("max_budget = %g, want 0", item.MaxBudget)
	}
}

func TestBudget_ToolRunner_Extras(t *testing.T) {
	payload := []byte(`{"object":"usage.budget","max_budget":10,"spend":1,"remaining":9,` +
		`"extras":{"team_id":"t-1","instance_id":"i-42"}}`)
	b := newToolRunnerBackend("myplugin", map[string]toolResult{
		"budget": {out: payload},
	})
	base := startServerN(t, b)

	resp := httpGet(t, base+"/v1/usage/budget?backend=myplugin") //nolint:bodyclose
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var item struct {
		Extras map[string]string `json:"extras"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&item); err != nil {
		t.Fatal(err)
	}
	if item.Extras["team_id"] != "t-1" || item.Extras["instance_id"] != "i-42" {
		t.Errorf("extras = %v", item.Extras)
	}
}

func TestBudget_MixedBackends_AggregateSkipsUnsupported(t *testing.T) {
	// One backend has BudgetReporter, one has a budget tool, one has neither.
	budReporter := newBudgetBackend("reporter", backend.Budget{
		Currency: "usd", MaxBudget: 100, Spend: 10, Remaining: 90,
	})
	toolRunner := newToolRunnerBackend("tooler", map[string]toolResult{
		"budget": {out: validBudgetJSON("eur", 200, 20, 180)},
	})
	plain := newFakeBackend("plain", []string{"gpt-4o"})
	base := startServerN(t, budReporter, plain, toolRunner)

	resp := httpGet(t, base+"/v1/usage/budget") //nolint:bodyclose
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Object string `json:"object"`
		Data   []struct {
			Backend string `json:"backend"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data) != 2 {
		t.Fatalf("expected 2 items (reporter + tooler), got %d: %+v", len(out.Data), out.Data)
	}
	seen := map[string]bool{}
	for _, d := range out.Data {
		seen[d.Backend] = true
	}
	if !seen["reporter"] || !seen["tooler"] {
		t.Errorf("expected reporter and tooler, got %v", seen)
	}
	if seen["plain"] {
		t.Error("plain backend should be absent from budget response")
	}
}
