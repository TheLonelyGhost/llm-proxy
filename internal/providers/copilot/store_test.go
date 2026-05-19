package copilot_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thelonelyghost/llm-proxy/internal/providers/copilot"
)

func TestFileTokenStore_RoundTripGitHub(t *testing.T) {
	dir := t.TempDir()
	s := copilot.NewFileTokenStoreAt(filepath.Join(dir, "cfg"), filepath.Join(dir, "cache"))
	if _, err := s.LoadGitHubToken(); err == nil {
		t.Fatal("expected not-authenticated")
	}
	want := &copilot.GitHubToken{AccessToken: "tok", TokenType: "bearer"}
	if err := s.SaveGitHubToken(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadGitHubToken()
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "tok" {
		t.Errorf("got = %+v", got)
	}
}

func TestFileTokenStore_RoundTripCopilot(t *testing.T) {
	dir := t.TempDir()
	s := copilot.NewFileTokenStoreAt(filepath.Join(dir, "cfg"), filepath.Join(dir, "cache"))
	if t1, err := s.LoadCopilotToken(); err != nil || t1 != nil {
		t.Fatalf("expected (nil,nil) for missing, got (%+v, %v)", t1, err)
	}
	if err := s.SaveCopilotToken(&copilot.CopilotToken{Token: "c", ExpiresAt: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadCopilotToken()
	if err != nil || got == nil {
		t.Fatalf("got %v %v", got, err)
	}
	if err := s.DeleteCopilotToken(); err != nil {
		t.Fatal(err)
	}
	// Deleting again must succeed (idempotent).
	if err := s.DeleteCopilotToken(); err != nil {
		t.Fatal(err)
	}
}

func TestFileTokenStore_DeleteAll(t *testing.T) {
	dir := t.TempDir()
	s := copilot.NewFileTokenStoreAt(filepath.Join(dir, "cfg"), filepath.Join(dir, "cache"))
	if err := s.SaveGitHubToken(&copilot.GitHubToken{AccessToken: "g"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCopilotToken(&copilot.CopilotToken{Token: "c", ExpiresAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAll(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadGitHubToken(); err == nil {
		t.Error("github token still present")
	}
	if got, err := s.LoadCopilotToken(); err != nil || got != nil {
		t.Errorf("copilot token still present: %v %v", got, err)
	}
}

func TestFileTokenStore_LoadCorrupt(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "cfg")
	cache := filepath.Join(dir, "cache")
	s := copilot.NewFileTokenStoreAt(cfg, cache)
	// Pre-create the cfg dir & write garbage.
	if err := writeAll(filepath.Join(cfg, "github_token.json"), []byte("not-json")); err != nil {
		// dir doesn't exist yet - create via Save first.
		_ = s.SaveGitHubToken(&copilot.GitHubToken{AccessToken: "x"})
		_ = writeAll(filepath.Join(cfg, "github_token.json"), []byte("not-json"))
	}
	if _, err := s.LoadGitHubToken(); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Errorf("expected decode error, got %v", err)
	}
}

func TestCopilotToken_Expired(t *testing.T) {
	tok := copilot.CopilotToken{ExpiresAt: time.Now().Add(time.Minute).Unix()}
	if tok.Expired(time.Now(), 0) {
		t.Error("not expired yet")
	}
	if !tok.Expired(time.Now(), 2*time.Minute) {
		t.Error("should be expired with skew")
	}
}
