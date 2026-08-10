package lockfile_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thelonelyghost/llm-proxy/internal/lockfile"
)

// ---- helpers ----------------------------------------------------------------

func writeLockFile(t *testing.T, dir, content string) string {
	t.Helper()
	p := filepath.Join(dir, lockfile.FileName)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func currentPlatform() string {
	return runtime.GOOS + "_" + runtime.GOARCH
}

// ---- PathFor ----------------------------------------------------------------

func TestPathFor_File(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.hcl")
	if err := os.WriteFile(cfgFile, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := lockfile.PathFor(cfgFile)
	if err != nil {
		t.Fatalf("PathFor: %v", err)
	}
	want := filepath.Join(dir, lockfile.FileName)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPathFor_Directory(t *testing.T) {
	dir := t.TempDir()
	got, err := lockfile.PathFor(dir)
	if err != nil {
		t.Fatalf("PathFor: %v", err)
	}
	want := filepath.Join(dir, lockfile.FileName)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPathFor_NonExistent(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "subdir", "config.hcl")
	got, err := lockfile.PathFor(fake)
	if err != nil {
		t.Fatalf("PathFor: %v", err)
	}
	// Should still return a sensible path adjacent to the (non-existent) file.
	if !strings.HasSuffix(got, lockfile.FileName) {
		t.Errorf("got %q, want path ending in %s", got, lockfile.FileName)
	}
}

// ---- Read -------------------------------------------------------------------

func TestRead_NotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := lockfile.Read(filepath.Join(dir, lockfile.FileName))
	if err == nil {
		t.Fatal("expected ErrNotFound, got nil")
	}
	if !errors.Is(err, lockfile.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestRead_Empty(t *testing.T) {
	dir := t.TempDir()
	p := writeLockFile(t, dir, "")
	lf, err := lockfile.Read(p)
	if err != nil {
		t.Fatalf("Read empty: %v", err)
	}
	if len(lf.Entries()) != 0 {
		t.Errorf("expected 0 entries, got %d", len(lf.Entries()))
	}
}

func TestRead_OneEntry(t *testing.T) {
	dir := t.TempDir()
	p := writeLockFile(t, dir, `
plugin "myplugin" {
  source   = "github.com/example/llm-proxy-provider-myplugin"
  version  = "~> 1.0"
  resolved = "1.2.3"
  platform = "linux_amd64"
}
`)
	lf, err := lockfile.Read(p)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	entries := lf.Entries()
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.Name != "myplugin" {
		t.Errorf("Name = %q, want %q", e.Name, "myplugin")
	}
	if e.Source != "github.com/example/llm-proxy-provider-myplugin" {
		t.Errorf("Source = %q", e.Source)
	}
	if e.Constraint != "~> 1.0" {
		t.Errorf("Constraint = %q", e.Constraint)
	}
	if e.Resolved != "1.2.3" {
		t.Errorf("Resolved = %q", e.Resolved)
	}
	if e.Platform != "linux_amd64" {
		t.Errorf("Platform = %q", e.Platform)
	}
}

func TestRead_MultipleEntries(t *testing.T) {
	dir := t.TempDir()
	p := writeLockFile(t, dir, `
plugin "alpha" {
  source   = "github.com/org/llm-proxy-provider-alpha"
  version  = "~> 1.0"
  resolved = "1.0.0"
  platform = "linux_amd64"
}

plugin "beta" {
  source   = "github.com/org/llm-proxy-provider-beta"
  version  = "1.5.0"
  resolved = "1.5.0"
  platform = "darwin_arm64"
}
`)
	lf, err := lockfile.Read(p)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	entries := lf.Entries()
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
}

func TestRead_InvalidHCL(t *testing.T) {
	dir := t.TempDir()
	p := writeLockFile(t, dir, `not valid hcl @@@`)
	_, err := lockfile.Read(p)
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
}

// ---- Write ------------------------------------------------------------------

func TestWrite_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	lf := &lockfile.Lockfile{}
	lf.Set(lockfile.Entry{
		Name:       "myplugin",
		Source:     "github.com/example/llm-proxy-provider-myplugin",
		Constraint: "~> 1.0",
		Resolved:   "1.2.3",
	})

	path := filepath.Join(dir, lockfile.FileName)
	if err := lockfile.Write(path, lf); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := lockfile.Read(path)
	if err != nil {
		t.Fatalf("Read after Write: %v", err)
	}
	entries := got.Entries()
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.Name != "myplugin" {
		t.Errorf("Name = %q", e.Name)
	}
	if e.Resolved != "1.2.3" {
		t.Errorf("Resolved = %q", e.Resolved)
	}
	if e.Platform != currentPlatform() {
		t.Errorf("Platform = %q, want %q", e.Platform, currentPlatform())
	}
}

func TestWrite_Deterministic(t *testing.T) {
	// Writing the same Lockfile twice produces identical bytes.
	dir := t.TempDir()
	lf := &lockfile.Lockfile{}
	lf.Set(lockfile.Entry{
		Name:       "beta",
		Source:     "github.com/org/llm-proxy-provider-beta",
		Constraint: "~> 1.0",
		Resolved:   "1.0.1",
	})
	lf.Set(lockfile.Entry{
		Name:       "alpha",
		Source:     "github.com/org/llm-proxy-provider-alpha",
		Constraint: "1.5.0",
		Resolved:   "1.5.0",
	})

	p := filepath.Join(dir, lockfile.FileName)
	if err := lockfile.Write(p, lf); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(p)
	if err := lockfile.Write(p, lf); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(p)
	if string(first) != string(second) {
		t.Errorf("write not deterministic:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestWrite_SortedByName(t *testing.T) {
	dir := t.TempDir()
	lf := &lockfile.Lockfile{}
	lf.Set(lockfile.Entry{
		Name: "zzz", Source: "github.com/a/b", Constraint: "1.0.0", Resolved: "1.0.0",
	})
	lf.Set(lockfile.Entry{
		Name: "aaa", Source: "github.com/c/d", Constraint: "1.0.0", Resolved: "1.0.0",
	})

	p := filepath.Join(dir, lockfile.FileName)
	if err := lockfile.Write(p, lf); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	iAAA := strings.Index(string(data), `"aaa"`)
	iZZZ := strings.Index(string(data), `"zzz"`)
	if iAAA >= iZZZ {
		t.Errorf("entries not sorted by name: aaa at %d, zzz at %d\n%s", iAAA, iZZZ, data)
	}
}

func TestWrite_ContainsHeader(t *testing.T) {
	dir := t.TempDir()
	lf := &lockfile.Lockfile{}
	p := filepath.Join(dir, lockfile.FileName)
	if err := lockfile.Write(p, lf); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), "DO NOT EDIT BY HAND") {
		t.Errorf("lock file missing header: %s", data)
	}
}

func TestWrite_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "nested", "subdir", lockfile.FileName)
	lf := &lockfile.Lockfile{}
	if err := lockfile.Write(p, lf); err != nil {
		t.Fatalf("Write with missing parent dir: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("lock file not created: %v", err)
	}
}

// ---- Lookup / Set -----------------------------------------------------------

func TestLookup_NotFound(t *testing.T) {
	lf := &lockfile.Lockfile{}
	_, ok := lf.Lookup("nonexistent")
	if ok {
		t.Error("expected Lookup to return false for missing entry")
	}
}

func TestLookup_Found(t *testing.T) {
	lf := &lockfile.Lockfile{}
	lf.Set(lockfile.Entry{
		Name:       "myplugin",
		Source:     "github.com/example/llm-proxy-provider-myplugin",
		Constraint: "~> 1.0",
		Resolved:   "1.2.3",
	})
	e, ok := lf.Lookup("myplugin")
	if !ok {
		t.Fatal("expected Lookup to find entry")
	}
	if e.Resolved != "1.2.3" {
		t.Errorf("Resolved = %q, want %q", e.Resolved, "1.2.3")
	}
	if e.Platform != currentPlatform() {
		t.Errorf("Platform = %q, want %q", e.Platform, currentPlatform())
	}
}

func TestSet_UpdatesExisting(t *testing.T) {
	lf := &lockfile.Lockfile{}
	lf.Set(lockfile.Entry{
		Name: "myplugin", Source: "github.com/a/b", Constraint: "~> 1.0", Resolved: "1.0.0",
	})
	lf.Set(lockfile.Entry{
		Name: "myplugin", Source: "github.com/a/b", Constraint: "~> 1.0", Resolved: "1.2.3",
	})
	entries := lf.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after update, got %d", len(entries))
	}
	if entries[0].Resolved != "1.2.3" {
		t.Errorf("Resolved = %q, want %q", entries[0].Resolved, "1.2.3")
	}
}

func TestSet_MultiPlatformEntries(t *testing.T) {
	// Simulate loading a lockfile that already has an entry for another
	// platform, then setting an entry for the current one.
	dir := t.TempDir()
	otherPlatform := "plan9_386"
	p := writeLockFile(t, dir, `
plugin "myplugin" {
  source   = "github.com/example/llm-proxy-provider-myplugin"
  version  = "~> 1.0"
  resolved = "1.0.0"
  platform = "`+otherPlatform+`"
}
`)
	lf, err := lockfile.Read(p)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	// Add an entry for the current platform.
	lf.Set(lockfile.Entry{
		Name:       "myplugin",
		Source:     "github.com/example/llm-proxy-provider-myplugin",
		Constraint: "~> 1.0",
		Resolved:   "1.2.3",
	})

	entries := lf.Entries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (one per platform), got %d", len(entries))
	}
	// The other platform's resolved version should be untouched.
	var foundOther bool
	for _, e := range entries {
		if e.Platform == otherPlatform {
			foundOther = true
			if e.Resolved != "1.0.0" {
				t.Errorf("other platform Resolved = %q, want %q", e.Resolved, "1.0.0")
			}
		}
	}
	if !foundOther {
		t.Error("entry for other platform disappeared after Set")
	}
}

// ---- NormalisePlatform -------------------------------------------------------

func TestNormalisePlatform(t *testing.T) {
	if got := lockfile.NormalisePlatform("darwin", "arm64"); got != "darwin_arm64" {
		t.Errorf("got %q", got)
	}
	if got := lockfile.NormalisePlatform("Linux", "AMD64"); got != "linux_amd64" {
		t.Errorf("got %q", got)
	}
}

// ---- Checksum field ---------------------------------------------------------

// TestWrite_RoundTrip_WithChecksum verifies that a non-empty Checksum field
// survives a Write→Read round-trip.
func TestWrite_RoundTrip_WithChecksum(t *testing.T) {
	dir := t.TempDir()
	lf := &lockfile.Lockfile{}
	lf.Set(lockfile.Entry{
		Name:       "myplugin",
		Source:     "github.com/example/llm-proxy-provider-myplugin",
		Constraint: "~> 1.0",
		Resolved:   "1.2.3",
		Checksum:   "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	})

	path := filepath.Join(dir, lockfile.FileName)
	if err := lockfile.Write(path, lf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := lockfile.Read(path)
	if err != nil {
		t.Fatalf("Read after Write: %v", err)
	}
	entries := got.Entries()
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].Checksum != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("Checksum = %q, want e3b0c...", entries[0].Checksum)
	}
}

// TestRead_EntryWithChecksum verifies that an existing lock file that already
// carries a checksum attribute is parsed correctly.
func TestRead_EntryWithChecksum(t *testing.T) {
	dir := t.TempDir()
	p := writeLockFile(t, dir, `
plugin "myplugin" {
  source   = "github.com/example/llm-proxy-provider-myplugin"
  version  = "~> 1.0"
  resolved = "1.2.3"
  platform = "linux_amd64"
  checksum = "deadbeef"
}
`)
	lf, err := lockfile.Read(p)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	entries := lf.Entries()
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].Checksum != "deadbeef" {
		t.Errorf("Checksum = %q, want deadbeef", entries[0].Checksum)
	}
}

// TestWrite_ChecksumOmittedWhenEmpty verifies that when Checksum is empty,
// no checksum attribute appears in the written lock file (backwards compat).
func TestWrite_ChecksumOmittedWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	lf := &lockfile.Lockfile{}
	lf.Set(lockfile.Entry{
		Name:     "myplugin",
		Source:   "github.com/example/llm-proxy-provider-myplugin",
		Resolved: "1.0.0",
	})
	p := filepath.Join(dir, lockfile.FileName)
	if err := lockfile.Write(p, lf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	data, _ := os.ReadFile(p)
	if strings.Contains(string(data), "checksum") {
		t.Errorf("lock file should not contain 'checksum' when empty:\n%s", data)
	}
}
