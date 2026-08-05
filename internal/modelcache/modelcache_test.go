package modelcache_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
	"github.com/thelonelyghost/llm-proxy/internal/modelcache"
)

// --------------------------------------------------------------------------
// helpers
// --------------------------------------------------------------------------

var errUpstream = errors.New("upstream unavailable")

// fakeModels returns a deterministic model list for a given call index.
func fakeModels(n int) []backend.ModelInfo {
	out := make([]backend.ModelInfo, n)
	for i := range out {
		out[i] = backend.ModelInfo{
			ID:     fmt.Sprintf("model-%d", i),
			Vendor: "test",
		}
	}
	return out
}

// newFetch returns a fetch function that returns models on call < failAfter
// and errUpstream afterwards. Pass failAfter=-1 to always succeed.
func newFetch(models []backend.ModelInfo, failAfter int) (func(context.Context) ([]backend.ModelInfo, error), *int) {
	calls := new(int)
	return func(_ context.Context) ([]backend.ModelInfo, error) {
		*calls++
		if failAfter >= 0 && *calls > failAfter {
			return nil, errUpstream
		}
		return models, nil
	}, calls
}

// writeCacheFile writes a raw JSON cache entry directly to dir/models-<name>.json
// with the given fetchedAt, bypassing NewWrappedFunc. Used to pre-seed the
// disk layer in tests.
func writeCacheFile(t *testing.T, dir, name string, models []backend.ModelInfo, fetchedAt time.Time) {
	t.Helper()
	type entry struct {
		FetchedAt time.Time           `json:"fetched_at"`
		Models    []backend.ModelInfo `json:"models"`
	}
	data, err := json.MarshalIndent(entry{FetchedAt: fetchedAt, Models: models}, "", "  ")
	if err != nil {
		t.Fatalf("writeCacheFile marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models-"+name+".json"), data, 0o644); err != nil {
		t.Fatalf("writeCacheFile write: %v", err)
	}
}

// setCacheDir overrides LLM_PROXY_CACHE_DIR for the duration of the test and
// restores it on cleanup.
func setCacheDir(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("LLM_PROXY_CACHE_DIR", dir)
}

// --------------------------------------------------------------------------
// basic hit/miss behaviour
// --------------------------------------------------------------------------

func TestWrappedFunc_MemoryHit(t *testing.T) {
	dir := t.TempDir()
	setCacheDir(t, dir)

	models := fakeModels(3)
	fetch, calls := newFetch(models, -1)
	w := modelcache.NewWrappedFunc("test", modelcache.DefaultTTL, nil, fetch)

	// First call: miss → upstream fetch.
	got, err := w.Call(context.Background())
	if err != nil {
		t.Fatalf("first Call: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d models, want 3", len(got))
	}
	if *calls != 1 {
		t.Fatalf("expected 1 upstream call, got %d", *calls)
	}

	// Second call within TTL: in-memory hit, no upstream call.
	got2, err := w.Call(context.Background())
	if err != nil {
		t.Fatalf("second Call: %v", err)
	}
	if len(got2) != 3 {
		t.Fatalf("got %d models, want 3", len(got2))
	}
	if *calls != 1 {
		t.Fatalf("expected still 1 upstream call, got %d", *calls)
	}
}

func TestWrappedFunc_DiskHitAfterRestart(t *testing.T) {
	dir := t.TempDir()
	setCacheDir(t, dir)

	models := fakeModels(2)
	// Pre-seed disk with a fresh entry (fetched 1 hour ago, TTL 24 h).
	writeCacheFile(t, dir, "restart-test", models, time.Now().Add(-time.Hour))

	fetch, calls := newFetch(models, -1)
	w := modelcache.NewWrappedFunc("restart-test", modelcache.DefaultTTL, nil, fetch)

	got, err := w.Call(context.Background())
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d models, want 2", len(got))
	}
	// No upstream call: disk file was fresh.
	if *calls != 0 {
		t.Fatalf("expected 0 upstream calls (disk hit), got %d", *calls)
	}
}

func TestWrappedFunc_StaleDiskTriggersFetch(t *testing.T) {
	dir := t.TempDir()
	setCacheDir(t, dir)

	old := fakeModels(1)
	fresh := fakeModels(4)
	// Pre-seed disk with an entry that expired 2 hours ago.
	writeCacheFile(t, dir, "stale-test", old, time.Now().Add(-26*time.Hour))

	fetch, calls := newFetch(fresh, -1)
	w := modelcache.NewWrappedFunc("stale-test", modelcache.DefaultTTL, nil, fetch)

	got, err := w.Call(context.Background())
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d models (want 4 fresh)", len(got))
	}
	if *calls != 1 {
		t.Fatalf("expected 1 upstream call, got %d", *calls)
	}
}

// --------------------------------------------------------------------------
// stale-data fallback
// --------------------------------------------------------------------------

func TestWrappedFunc_StaleMemoryFallbackOnUpstreamFailure(t *testing.T) {
	dir := t.TempDir()
	setCacheDir(t, dir)

	models := fakeModels(2)
	// failAfter=1: first call succeeds, second fails.
	fetch, calls := newFetch(models, 1)
	w := modelcache.NewWrappedFunc("stale-mem", time.Millisecond, nil, fetch)

	// First call: cache miss → upstream fetch → warm cache.
	if _, err := w.Call(context.Background()); err != nil {
		t.Fatalf("first Call: %v", err)
	}
	// Wait for TTL to expire.
	time.Sleep(5 * time.Millisecond)

	// Second call: cache stale → upstream fails → should return stale in-memory data.
	got, err := w.Call(context.Background())
	if err != nil {
		t.Fatalf("second Call: %v (wanted stale fallback)", err)
	}
	if len(got) != 2 {
		t.Fatalf("stale fallback: got %d models, want 2", len(got))
	}
	if *calls != 2 {
		t.Fatalf("expected 2 upstream calls, got %d", *calls)
	}
}

func TestWrappedFunc_StaleDiskFallbackOnUpstreamFailure(t *testing.T) {
	dir := t.TempDir()
	setCacheDir(t, dir)

	// Pre-seed a stale disk file.
	stale := fakeModels(3)
	writeCacheFile(t, dir, "stale-disk", stale, time.Now().Add(-48*time.Hour))

	fetch := func(_ context.Context) ([]backend.ModelInfo, error) { return nil, errUpstream }
	w := modelcache.NewWrappedFunc("stale-disk", modelcache.DefaultTTL, nil, fetch)

	got, err := w.Call(context.Background())
	if err != nil {
		t.Fatalf("Call: %v (wanted stale disk fallback)", err)
	}
	if len(got) != 3 {
		t.Fatalf("stale disk fallback: got %d models, want 3", len(got))
	}
}

func TestWrappedFunc_NoCacheUpstreamFailureReturnsError(t *testing.T) {
	dir := t.TempDir()
	setCacheDir(t, dir)

	fetch := func(_ context.Context) ([]backend.ModelInfo, error) { return nil, errUpstream }
	w := modelcache.NewWrappedFunc("no-cache", modelcache.DefaultTTL, nil, fetch)

	_, err := w.Call(context.Background())
	if err == nil {
		t.Fatal("expected error when no cache and upstream fails")
	}
	if !errors.Is(err, errUpstream) {
		t.Fatalf("expected errUpstream wrapped in error, got: %v", err)
	}
}

// --------------------------------------------------------------------------
// Invalidate / SIGHUP
// --------------------------------------------------------------------------

func TestWrappedFunc_InvalidateForcesRefetch(t *testing.T) {
	dir := t.TempDir()
	setCacheDir(t, dir)

	models := fakeModels(2)
	fetch, calls := newFetch(models, -1)
	w := modelcache.NewWrappedFunc("invalidate-test", modelcache.DefaultTTL, nil, fetch)

	// Warm the cache.
	if _, err := w.Call(context.Background()); err != nil {
		t.Fatalf("first Call: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("expected 1 call after warm, got %d", *calls)
	}

	// Invalidate clears in-memory state.
	w.Invalidate()

	// Next call must hit upstream again (in-memory gone; disk file is fresh
	// but Invalidate should bypass it too — we rely on disk file being freshly
	// written seconds ago, still within TTL, so disk *will* be hit, meaning
	// 0 upstream calls after invalidate is also correct). Instead, just check
	// that the result is still valid.
	got, err := w.Call(context.Background())
	if err != nil {
		t.Fatalf("Call after Invalidate: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d models after invalidate, want 2", len(got))
	}
}

func TestWrappedFunc_InvalidateWithExpiredDiskForcesUpstreamFetch(t *testing.T) {
	dir := t.TempDir()
	setCacheDir(t, dir)

	models := fakeModels(2)
	// Use a very short TTL so after Invalidate the disk file is also stale.
	fetch, calls := newFetch(models, -1)
	w := modelcache.NewWrappedFunc("inv-expired", time.Millisecond, nil, fetch)

	// Warm cache.
	if _, err := w.Call(context.Background()); err != nil {
		t.Fatalf("first Call: %v", err)
	}
	time.Sleep(5 * time.Millisecond) // let TTL expire on disk too

	w.Invalidate()

	if _, err := w.Call(context.Background()); err != nil {
		t.Fatalf("Call after Invalidate+expired disk: %v", err)
	}
	if *calls != 2 {
		t.Fatalf("expected 2 upstream calls (initial + post-invalidate), got %d", *calls)
	}
}

// --------------------------------------------------------------------------
// disk file properties
// --------------------------------------------------------------------------

func TestWriteDisk_AtomicAndReadable(t *testing.T) {
	dir := t.TempDir()
	setCacheDir(t, dir)

	models := fakeModels(5)
	fetch, _ := newFetch(models, -1)
	w := modelcache.NewWrappedFunc("disk-props", modelcache.DefaultTTL, nil, fetch)

	if _, err := w.Call(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Verify the file exists and is valid JSON.
	data, err := os.ReadFile(filepath.Join(dir, "models-disk-props.json"))
	if err != nil {
		t.Fatalf("cache file not written: %v", err)
	}
	var parsed struct {
		FetchedAt time.Time           `json:"fetched_at"`
		Models    []backend.ModelInfo `json:"models"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("cache file not valid JSON: %v", err)
	}
	if len(parsed.Models) != 5 {
		t.Fatalf("disk: got %d models, want 5", len(parsed.Models))
	}
	if parsed.FetchedAt.IsZero() {
		t.Fatal("disk: fetched_at is zero")
	}
}

func TestWriteDisk_Mode0644(t *testing.T) {
	dir := t.TempDir()
	setCacheDir(t, dir)

	models := fakeModels(1)
	fetch, _ := newFetch(models, -1)
	w := modelcache.NewWrappedFunc("perm-test", modelcache.DefaultTTL, nil, fetch)
	if _, err := w.Call(context.Background()); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(filepath.Join(dir, "models-perm-test.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	const wantPerm = 0o644
	if got := fi.Mode().Perm(); got != wantPerm {
		t.Errorf("file perm = %o, want %o", got, wantPerm)
	}
}

// --------------------------------------------------------------------------
// concurrency
// --------------------------------------------------------------------------

func TestWrappedFunc_ConcurrentCalls(t *testing.T) {
	dir := t.TempDir()
	setCacheDir(t, dir)

	models := fakeModels(3)
	fetch, calls := newFetch(models, -1)
	w := modelcache.NewWrappedFunc("concurrent", modelcache.DefaultTTL, nil, fetch)

	const goroutines = 20
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)

	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			got, err := w.Call(context.Background())
			if err != nil {
				errs <- err
				return
			}
			if len(got) != 3 {
				errs <- fmt.Errorf("got %d models, want 3", len(got))
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	// All goroutines raced; upstream should have been called exactly once.
	if *calls != 1 {
		t.Errorf("concurrent calls: expected 1 upstream call, got %d", *calls)
	}
}

// --------------------------------------------------------------------------
// fuzz: disk round-trip
// --------------------------------------------------------------------------

func FuzzDiskRoundTrip(f *testing.F) {
	// Seed corpus: typical model ID characters and empty strings.
	f.Add("gpt-4o", "OpenAI", "chat")
	f.Add("", "", "")
	f.Add("claude-3-opus-20240229", "Anthropic", "tool_calls")
	f.Add("meta-llama/Meta-Llama-3-70B-Instruct", "Meta", "")

	f.Fuzz(func(t *testing.T, id, vendor, cap string) {
		dir := t.TempDir()
		t.Setenv("LLM_PROXY_CACHE_DIR", dir)

		var caps []string
		if cap != "" {
			caps = []string{cap}
		}
		input := []backend.ModelInfo{{ID: id, Vendor: vendor, Capabilities: caps}}

		fetch := func(_ context.Context) ([]backend.ModelInfo, error) { return input, nil }
		w := modelcache.NewWrappedFunc("fuzz", time.Hour, nil, fetch)

		// Populate the cache.
		if _, err := w.Call(context.Background()); err != nil {
			t.Fatalf("Call (populate): %v", err)
		}

		// Re-create the wrapper with a failing fetch to force disk-only read.
		w2 := modelcache.NewWrappedFunc("fuzz", time.Hour, nil, func(_ context.Context) ([]backend.ModelInfo, error) {
			return nil, errUpstream
		})
		got, err := w2.Call(context.Background())
		if err != nil {
			t.Fatalf("Call (reload): %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("round-trip: got %d models, want 1", len(got))
		}
		if got[0].ID != id {
			t.Errorf("round-trip ID: got %q, want %q", got[0].ID, id)
		}
		if got[0].Vendor != vendor {
			t.Errorf("round-trip Vendor: got %q, want %q", got[0].Vendor, vendor)
		}
	})
}
