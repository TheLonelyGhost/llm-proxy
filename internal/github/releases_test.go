package github_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/thelonelyghost/llm-proxy/internal/github"
)

type fakeRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// newTestServer returns an httptest.Server that serves releases as a GitHub-like
// paginated JSON response and an *http.Client pre-wired to hit that server.
//
// The first page returns all releases; subsequent pages return an empty array
// (simulating end-of-pagination).
func newTestServer(t *testing.T, releases []fakeRelease) (*httptest.Server, *http.Client) {
	t.Helper()
	first, _ := json.Marshal(releases) //nolint:errcheck
	empty, _ := json.Marshal([]fakeRelease{})

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
	client := clientForServer(t, srv)
	return srv, client
}

// clientForServer returns an *http.Client whose transport rewrites all
// outbound requests to hit srv regardless of the original host.
func clientForServer(t *testing.T, srv *httptest.Server) *http.Client {
	t.Helper()
	return &http.Client{
		Transport: &rewriteTransport{
			target: srv.URL,
			inner:  http.DefaultTransport,
		},
	}
}

// rewriteTransport redirects every request to a fixed target URL, replacing
// only the scheme and host while preserving the path and query.
type rewriteTransport struct {
	target string // e.g. "http://127.0.0.1:12345"
	inner  http.RoundTripper
}

func (rt *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	// Parse the target URL and inject scheme+host.
	cloned.URL.Scheme = "http"
	// target is "http://host:port" — extract just the host:port.
	const prefix = "http://"
	if len(rt.target) > len(prefix) {
		cloned.URL.Host = rt.target[len(prefix):]
	}
	return rt.inner.RoundTrip(cloned)
}

// TestListReleaseTags_Basic verifies that normal non-draft, non-prerelease
// tags with a v<digit> prefix are returned.
func TestListReleaseTags_Basic(t *testing.T) {
	releases := []fakeRelease{
		{TagName: "v1.2.0"},
		{TagName: "v1.1.0"},
		{TagName: "v1.0.0"},
	}
	_, client := newTestServer(t, releases)
	tags, err := github.ListReleaseTags(context.Background(), client, "owner/repo")
	if err != nil {
		t.Fatalf("ListReleaseTags: %v", err)
	}
	if len(tags) != 3 {
		t.Fatalf("got %d tags, want 3: %v", len(tags), tags)
	}
	for _, want := range []string{"v1.2.0", "v1.1.0", "v1.0.0"} {
		found := false
		for _, got := range tags {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("tag %q not found in %v", want, tags)
		}
	}
}

// TestListReleaseTags_FiltersDrafts verifies that draft releases are excluded.
func TestListReleaseTags_FiltersDrafts(t *testing.T) {
	releases := []fakeRelease{
		{TagName: "v2.0.0", Draft: true},
		{TagName: "v1.0.0"},
	}
	_, client := newTestServer(t, releases)
	tags, err := github.ListReleaseTags(context.Background(), client, "owner/repo")
	if err != nil {
		t.Fatalf("ListReleaseTags: %v", err)
	}
	if len(tags) != 1 || tags[0] != "v1.0.0" {
		t.Errorf("got %v, want [v1.0.0]", tags)
	}
}

// TestListReleaseTags_FiltersPrerelease verifies that GitHub-flagged
// pre-releases are excluded.
func TestListReleaseTags_FiltersPrerelease(t *testing.T) {
	releases := []fakeRelease{
		{TagName: "v2.0.0-rc.1", Prerelease: true},
		{TagName: "v1.0.0"},
	}
	_, client := newTestServer(t, releases)
	tags, err := github.ListReleaseTags(context.Background(), client, "owner/repo")
	if err != nil {
		t.Fatalf("ListReleaseTags: %v", err)
	}
	if len(tags) != 1 || tags[0] != "v1.0.0" {
		t.Errorf("got %v, want [v1.0.0]", tags)
	}
}

// TestListReleaseTags_FiltersNonVTags verifies that tags not matching
// "v<digit>..." are excluded.
func TestListReleaseTags_FiltersNonVTags(t *testing.T) {
	releases := []fakeRelease{
		{TagName: "latest"},
		{TagName: "nightly"},
		{TagName: "v1.0.0"},
		{TagName: "release-2024"},
	}
	_, client := newTestServer(t, releases)
	tags, err := github.ListReleaseTags(context.Background(), client, "owner/repo")
	if err != nil {
		t.Fatalf("ListReleaseTags: %v", err)
	}
	if len(tags) != 1 || tags[0] != "v1.0.0" {
		t.Errorf("got %v, want [v1.0.0]", tags)
	}
}

// TestListReleaseTags_Empty verifies that an empty release list returns nil.
func TestListReleaseTags_Empty(t *testing.T) {
	_, client := newTestServer(t, []fakeRelease{})
	tags, err := github.ListReleaseTags(context.Background(), client, "owner/repo")
	if err != nil {
		t.Fatalf("ListReleaseTags: %v", err)
	}
	if len(tags) != 0 {
		t.Errorf("got %v, want empty", tags)
	}
}

// TestListReleaseTags_HTTPError verifies that a non-200 response is wrapped in
// ErrReleaseListFailed.
func TestListReleaseTags_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	client := clientForServer(t, srv)

	_, err := github.ListReleaseTags(context.Background(), client, "owner/repo")
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
	if !errors.Is(err, github.ErrReleaseListFailed) {
		t.Errorf("expected ErrReleaseListFailed, got %v", err)
	}
}

// TestListReleaseTags_NilClient verifies that passing nil uses the default
// client without panicking. We use a cancelled context to avoid real network
// traffic.
func TestListReleaseTags_NilClient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancelled
	_, err := github.ListReleaseTags(ctx, nil, "owner/repo")
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

// ---- FetchChecksumFile ------------------------------------------------------

// TestFetchChecksumFile_Basic verifies that a well-formed SHA256SUMS file is
// parsed into a filename→hex map.
func TestFetchChecksumFile_Basic(t *testing.T) {
	body := "abc123  file1.tar.gz\ndef456  file2.tar.gz\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	client := clientForServer(t, srv)

	got, err := github.FetchChecksumFile(context.Background(), client, srv.URL+"/SHA256SUMS")
	if err != nil {
		t.Fatalf("FetchChecksumFile: %v", err)
	}
	if got["file1.tar.gz"] != "abc123" {
		t.Errorf("file1.tar.gz = %q, want %q", got["file1.tar.gz"], "abc123")
	}
	if got["file2.tar.gz"] != "def456" {
		t.Errorf("file2.tar.gz = %q, want %q", got["file2.tar.gz"], "def456")
	}
}

// TestFetchChecksumFile_SkipsMalformedLines verifies that lines without exactly
// two whitespace-separated fields are silently skipped.
func TestFetchChecksumFile_SkipsMalformedLines(t *testing.T) {
	body := "goodhex  good.tar.gz\nbadline\n  \n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	client := clientForServer(t, srv)

	got, err := github.FetchChecksumFile(context.Background(), client, srv.URL+"/SHA256SUMS")
	if err != nil {
		t.Fatalf("FetchChecksumFile: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("got %d entries, want 1: %v", len(got), got)
	}
	if got["good.tar.gz"] != "goodhex" {
		t.Errorf("good.tar.gz = %q, want %q", got["good.tar.gz"], "goodhex")
	}
}

// TestFetchChecksumFile_HTTPError verifies that a non-200 response returns an
// error.
func TestFetchChecksumFile_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	client := clientForServer(t, srv)

	_, err := github.FetchChecksumFile(context.Background(), client, srv.URL+"/SHA256SUMS")
	if err == nil {
		t.Fatal("expected error for 404")
	}
}

// TestFetchChecksumFile_NilClient verifies that nil uses http.DefaultClient
// (exercises the nil-guard, cancelled context prevents real traffic).
func TestFetchChecksumFile_NilClient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := github.FetchChecksumFile(ctx, nil, "https://example.com/SHA256SUMS")
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}
