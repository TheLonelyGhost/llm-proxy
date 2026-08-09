package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thelonelyghost/llm-proxy/internal/cli"
	"github.com/thelonelyghost/llm-proxy/internal/config"
	"github.com/thelonelyghost/llm-proxy/internal/github"
	"github.com/thelonelyghost/llm-proxy/internal/lockfile"
	"github.com/thelonelyghost/llm-proxy/internal/plugin"
	"github.com/thelonelyghost/llm-proxy/internal/semver"
)

// fakeGHRelease mirrors the subset of the GitHub Releases API response used
// by ListReleaseTags.
type fakeGHRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// newGHStub returns an *http.Client backed by an httptest.Server that serves
// releases as a GitHub-like JSON array. The first page returns all releases;
// subsequent pages return an empty array to terminate pagination.
func newGHStub(t *testing.T, releases []fakeGHRelease) *http.Client {
	t.Helper()
	first, _ := json.Marshal(releases) //nolint:errcheck
	empty, _ := json.Marshal([]fakeGHRelease{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		page := r.URL.Query().Get("page")
		if page == "" || page == "1" {
			_, _ = w.Write(first)
		} else {
			_, _ = w.Write(empty)
		}
	}))
	t.Cleanup(srv.Close)
	return &http.Client{
		Transport: &stubTransport{base: srv.URL, inner: http.DefaultTransport},
	}
}

// stubTransport rewrites the host of every request to the stub server.
type stubTransport struct {
	base  string
	inner http.RoundTripper
}

func (st *stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.URL.Scheme = "http"
	cloned.URL.Host = strings.TrimPrefix(st.base, "http://")
	return st.inner.RoundTrip(cloned)
}

// ---- resolveVersion (low-level) tests ---------------------------------------

// TestResolveVersion_ExactPin verifies that an exact version constraint is
// returned without any network call.
func TestResolveVersion_ExactPin(t *testing.T) {
	// nil client: a real network call would fail, proving no call was made.
	got, err := cli.ResolveVersionForTest(context.Background(), nil,
		"file:///some/path", "1.2.3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "1.2.3" {
		t.Errorf("got %q, want %q", got, "1.2.3")
	}
}

// TestResolveVersion_ExactPin_WithOperator verifies "= 1.2.3" is exact.
func TestResolveVersion_ExactPin_WithOperator(t *testing.T) {
	got, err := cli.ResolveVersionForTest(context.Background(), nil,
		"file:///some/path", "= 1.2.3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "1.2.3" {
		t.Errorf("got %q, want %q", got, "1.2.3")
	}
}

// TestResolveVersion_RangeConstraint_GitHub verifies that a range constraint
// on a GitHub source queries the releases and selects the best match.
func TestResolveVersion_RangeConstraint_GitHub(t *testing.T) {
	releases := []fakeGHRelease{
		{TagName: "v1.0.0"},
		{TagName: "v1.1.0"},
		{TagName: "v1.9.0"},
		{TagName: "v2.0.0"}, // outside ~> 1.0
	}
	client := newGHStub(t, releases)
	got, err := cli.ResolveVersionForTest(context.Background(), client,
		"github.com/example/llm-proxy-provider-foo", "~> 1.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "1.9.0" {
		t.Errorf("got %q, want %q", got, "1.9.0")
	}
}

// TestResolveVersion_RangeConstraint_GitHub_Compound tests a compound constraint.
func TestResolveVersion_RangeConstraint_GitHub_Compound(t *testing.T) {
	releases := []fakeGHRelease{
		{TagName: "v0.9.0"},
		{TagName: "v1.0.0"},
		{TagName: "v1.5.0"},
		{TagName: "v2.0.0"},
	}
	client := newGHStub(t, releases)
	got, err := cli.ResolveVersionForTest(context.Background(), client,
		"github.com/example/llm-proxy-provider-foo", ">= 1.0.0, < 2.0.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "1.5.0" {
		t.Errorf("got %q, want %q", got, "1.5.0")
	}
}

// TestResolveVersion_NoMatchingRelease verifies ErrNoMatchingRelease when
// the constraint matches no release.
func TestResolveVersion_NoMatchingRelease(t *testing.T) {
	releases := []fakeGHRelease{
		{TagName: "v2.0.0"},
		{TagName: "v3.0.0"},
	}
	client := newGHStub(t, releases)
	_, err := cli.ResolveVersionForTest(context.Background(), client,
		"github.com/example/llm-proxy-provider-foo", "~> 1.0")
	if err == nil {
		t.Fatal("expected error for no matching release")
	}
	if !errors.Is(err, github.ErrNoMatchingRelease) {
		t.Errorf("expected ErrNoMatchingRelease, got %v", err)
	}
}

// TestResolveVersion_InvalidConstraint verifies that a bad constraint string
// surfaces an ErrInvalidConstraint error.
func TestResolveVersion_InvalidConstraint(t *testing.T) {
	_, err := cli.ResolveVersionForTest(context.Background(), nil,
		"file:///some/path", "not-a-version")
	if err == nil {
		t.Fatal("expected error for invalid constraint")
	}
	if !errors.Is(err, semver.ErrInvalidConstraint) && !errors.Is(err, semver.ErrInvalidVersion) {
		t.Errorf("expected semver error, got %v", err)
	}
}

// TestResolveVersion_NonGitHubRange verifies that range constraints on
// non-GitHub sources fail with a clear message.
func TestResolveVersion_NonGitHubRange(t *testing.T) {
	_, err := cli.ResolveVersionForTest(context.Background(), nil,
		"file:///some/path", "~> 1.0")
	if err == nil {
		t.Fatal("expected error for non-GitHub range constraint")
	}
	if !strings.Contains(err.Error(), "github.com") {
		t.Errorf("expected error to mention 'github.com', got: %v", err)
	}
}

// TestResolveVersion_InvalidGitHubSource verifies that a malformed
// github.com source URL is rejected.
func TestResolveVersion_InvalidGitHubSource(t *testing.T) {
	_, err := cli.ResolveVersionForTest(context.Background(), nil,
		"github.com/justowner", "~> 1.0")
	if err == nil {
		t.Fatal("expected error for incomplete github.com source")
	}
	if !strings.Contains(err.Error(), "owner") && !strings.Contains(err.Error(), "github.com") {
		t.Errorf("expected error mentioning source format, got: %v", err)
	}
}

// TestResolveVersion_SkipsDraftAndPrerelease verifies that draft and
// pre-release GitHub releases are not selected by a range constraint.
func TestResolveVersion_SkipsDraftAndPrerelease(t *testing.T) {
	releases := []fakeGHRelease{
		{TagName: "v1.1.0", Draft: true},
		{TagName: "v1.2.0", Prerelease: true},
		{TagName: "v1.0.0"},
	}
	client := newGHStub(t, releases)
	got, err := cli.ResolveVersionForTest(context.Background(), client,
		"github.com/example/llm-proxy-provider-foo", "~> 1.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "1.0.0" {
		t.Errorf("got %q, want %q", got, "1.0.0")
	}
}

// ---- resolvePluginVersion priority chain — normal mode (upgrade=false) ------

// makePB builds a minimal PluginBlock for tests.
func makePB(name, source, version string) config.PluginBlock {
	return config.PluginBlock{Name: name, Source: source, Version: version}
}

// seedCacheVersion creates a fake installed version in the given cache base.
func seedCacheVersion(t *testing.T, base, namespace, name, version string) {
	t.Helper()
	dir, err := plugin.PluginCacheDir(base, namespace, name, version)
	if err != nil {
		t.Fatalf("PluginCacheDir: %v", err)
	}
	binPath := filepath.Join(dir, plugin.BinaryName(name))
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
}

// TestResolvePluginVersion_Lockfile verifies that a lock file entry is used
// without any cache scan or network call.
func TestResolvePluginVersion_Lockfile(t *testing.T) {
	base := t.TempDir()
	pb := makePB("myplugin", "file://"+base, "~> 1.0")

	lf := &lockfile.Lockfile{}
	lf.Set(lockfile.Entry{
		Name:       "myplugin",
		Source:     pb.Source,
		Constraint: pb.Version,
		Resolved:   "1.5.0",
	})

	version, _, err := cli.ResolvePluginVersionForTest(
		context.Background(), pb, base, "ns", "myplugin", lf, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version != "1.5.0" {
		t.Errorf("got %q, want %q (should use lockfile)", version, "1.5.0")
	}
}

// TestResolvePluginVersion_CacheHit verifies that when no lock file entry
// exists but a matching version is cached, the cache is used (no network).
func TestResolvePluginVersion_CacheHit(t *testing.T) {
	base := t.TempDir()
	// Seed two versions in the cache; 1.2.0 satisfies ~> 1.0.
	seedCacheVersion(t, base, "ns", "myplugin", "1.2.0")
	seedCacheVersion(t, base, "ns", "myplugin", "0.9.0") // doesn't match ~> 1.0

	pb := makePB("myplugin", "github.com/example/llm-proxy-provider-myplugin", "~> 1.0")
	lf := &lockfile.Lockfile{} // empty

	version, _, err := cli.ResolvePluginVersionForTest(
		context.Background(), pb, base, "ns", "myplugin", lf, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version != "1.2.0" {
		t.Errorf("got %q, want %q (should use cache)", version, "1.2.0")
	}
}

// TestResolvePluginVersion_CacheHit_PicksBest verifies that the highest
// matching cached version is selected.
func TestResolvePluginVersion_CacheHit_PicksBest(t *testing.T) {
	base := t.TempDir()
	for _, v := range []string{"1.0.0", "1.3.0", "1.1.0"} {
		seedCacheVersion(t, base, "ns", "myplugin", v)
	}

	pb := makePB("myplugin", "github.com/example/llm-proxy-provider-myplugin", "~> 1.0")
	lf := &lockfile.Lockfile{}

	version, _, err := cli.ResolvePluginVersionForTest(
		context.Background(), pb, base, "ns", "myplugin", lf, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version != "1.3.0" {
		t.Errorf("got %q, want %q (should pick highest cached match)", version, "1.3.0")
	}
}

// TestResolvePluginVersion_CacheNoMatch verifies that when cached versions
// don't satisfy the constraint, the error from remote resolution surfaces.
func TestResolvePluginVersion_CacheNoMatch(t *testing.T) {
	base := t.TempDir()
	// Cache only has 0.x which doesn't satisfy ~> 1.0.
	seedCacheVersion(t, base, "ns", "myplugin", "0.9.0")

	pb := makePB("myplugin", "github.com/example/llm-proxy-provider-myplugin", "~> 1.0")
	lf := &lockfile.Lockfile{}

	// No network stub → the GitHub call will fail cleanly (cancelled context).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := cli.ResolvePluginVersionForTest(ctx, pb, base, "ns", "myplugin", lf, false)
	if err == nil {
		t.Fatal("expected error when cache has no match and network is unavailable")
	}
}

// TestResolvePluginVersion_ExactPin_NoLookup verifies that an exact pin
// bypasses both lockfile and cache scan.
func TestResolvePluginVersion_ExactPin_NoLookup(t *testing.T) {
	base := t.TempDir()
	// Cache has a different version seeded — should be ignored for exact pins.
	seedCacheVersion(t, base, "ns", "myplugin", "1.9.0")

	pb := makePB("myplugin", "file://"+base, "= 1.0.0")
	lf := &lockfile.Lockfile{}

	version, _, err := cli.ResolvePluginVersionForTest(
		context.Background(), pb, base, "ns", "myplugin", lf, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version != "1.0.0" {
		t.Errorf("got %q, want %q (exact pin should be honoured)", version, "1.0.0")
	}
}

// TestResolvePluginVersion_LockfilePrecedence verifies that the lock file
// version is preferred even when a different (higher) version is cached.
func TestResolvePluginVersion_LockfilePrecedence(t *testing.T) {
	base := t.TempDir()
	// Cache has a higher version.
	seedCacheVersion(t, base, "ns", "myplugin", "1.9.0")

	pb := makePB("myplugin", "github.com/example/llm-proxy-provider-myplugin", "~> 1.0")

	lf := &lockfile.Lockfile{}
	lf.Set(lockfile.Entry{
		Name:       "myplugin",
		Source:     pb.Source,
		Constraint: pb.Version,
		Resolved:   "1.2.0", // lower than cached, but lock file wins
	})

	version, _, err := cli.ResolvePluginVersionForTest(
		context.Background(), pb, base, "ns", "myplugin", lf, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version != "1.2.0" {
		t.Errorf("got %q, want %q (lock file should win)", version, "1.2.0")
	}
}

// ---- resolvePluginVersion — upgrade mode (upgrade=true) ---------------------

// TestResolvePluginVersion_Upgrade_IgnoresLockfile verifies that upgrade mode
// skips the lock file and resolves from the remote registry.
func TestResolvePluginVersion_Upgrade_IgnoresLockfile(t *testing.T) {
	// We can't inject an HTTP client into resolvePluginVersion directly, but
	// we can verify the lock file entry is NOT returned by using a
	// file:// exact-pin source that resolves deterministically without network.
	// Full remote-resolution coverage is in TestPlugin_Install_Upgrade_*.
	//
	// Invariant: with upgrade=true and an exact-pin, the lock file entry
	// (a different version) is ignored and the pin is returned.
	base := t.TempDir()
	pb := makePB("myplugin", "file://"+base, "= 1.5.0")

	lf := &lockfile.Lockfile{}
	lf.Set(lockfile.Entry{
		Name:     "myplugin",
		Source:   pb.Source,
		Resolved: "1.2.0", // locked at an older version
	})

	version, _, err := cli.ResolvePluginVersionForTest(
		context.Background(), pb, base, "ns", "myplugin", lf, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Upgrade with an exact pin should return the pin, ignoring the lock.
	if version != "1.5.0" {
		t.Errorf("got %q, want %q (upgrade should ignore lock file)", version, "1.5.0")
	}
}

// TestResolvePluginVersion_Upgrade_IgnoresCache verifies that upgrade mode
// skips the cache even when a compatible version is present, and falls through
// to remote resolution.
func TestResolvePluginVersion_Upgrade_IgnoresCache(t *testing.T) {
	base := t.TempDir()
	// Cache has 1.3.0, which satisfies ~> 1.0. In upgrade mode it must be
	// ignored and the remote consulted.  We simulate the remote being
	// unavailable (cancelled context) to confirm the cache was not used.
	seedCacheVersion(t, base, "ns", "myplugin", "1.3.0")

	pb := makePB("myplugin", "github.com/example/llm-proxy-provider-myplugin", "~> 1.0")
	lf := &lockfile.Lockfile{} // empty lock

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // force network failure
	_, _, err := cli.ResolvePluginVersionForTest(ctx, pb, base, "ns", "myplugin", lf, true)
	if err == nil {
		t.Fatal("expected network error when upgrade bypasses cache and network is unavailable")
	}
	// The error must come from the network layer, not from a cache scan.
	if strings.Contains(err.Error(), "scan cache") {
		t.Errorf("upgrade mode should not scan cache, got: %v", err)
	}
}

// TestResolvePluginVersion_Upgrade_IgnoresCacheAndLockfile verifies that
// upgrade mode skips both the lock file AND the cache simultaneously.
func TestResolvePluginVersion_Upgrade_IgnoresCacheAndLockfile(t *testing.T) {
	base := t.TempDir()
	seedCacheVersion(t, base, "ns", "myplugin", "1.3.0") // cached

	pb := makePB("myplugin", "github.com/example/llm-proxy-provider-myplugin", "~> 1.0")

	lf := &lockfile.Lockfile{}
	lf.Set(lockfile.Entry{
		Name:     "myplugin",
		Source:   pb.Source,
		Resolved: "1.3.0", // same as cache
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := cli.ResolvePluginVersionForTest(ctx, pb, base, "ns", "myplugin", lf, true)
	if err == nil {
		t.Fatal("expected network error — upgrade must bypass both lock and cache")
	}
}

// ---- ReadOrNewLockfile tests ------------------------------------------------

func TestReadOrNewLockfile_NonExistent(t *testing.T) {
	dir := t.TempDir()
	lf, err := cli.ReadOrNewLockfileForTest(filepath.Join(dir, lockfile.FileName))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lf.Entries()) != 0 {
		t.Errorf("expected empty lockfile, got %d entries", len(lf.Entries()))
	}
}

func TestReadOrNewLockfile_Existing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, lockfile.FileName)
	lf1 := &lockfile.Lockfile{}
	lf1.Set(lockfile.Entry{
		Name:       "myplugin",
		Source:     "github.com/a/b",
		Constraint: "~> 1.0",
		Resolved:   "1.2.3",
	})
	if err := lockfile.Write(p, lf1); err != nil {
		t.Fatal(err)
	}

	lf2, err := cli.ReadOrNewLockfileForTest(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entries := lf2.Entries()
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	// The entry platform matches the current runtime.
	if entries[0].Platform != runtime.GOOS+"_"+runtime.GOARCH {
		t.Errorf("Platform = %q, want %q", entries[0].Platform, runtime.GOOS+"_"+runtime.GOARCH)
	}
}
