package plugin_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thelonelyghost/llm-proxy/internal/plugin"
)

// TestPluginCacheDir_GitHubSource verifies that a GitHub source address
// produces the expected cache path layout.
func TestPluginCacheDir_GitHubSource(t *testing.T) {
	base := t.TempDir()
	got, err := plugin.PluginCacheDir(base, "thelonelyghost", "ibmbob", "1.2.0")
	if err != nil {
		t.Fatalf("PluginCacheDir: %v", err)
	}
	want := filepath.Join(base, "plugins", "thelonelyghost", "ibmbob", "1.2.0", runtime.GOOS+"_"+runtime.GOARCH)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestPluginCacheDir_CreatesDirectory verifies that PluginCacheDir creates
// the directory if it does not exist.
func TestPluginCacheDir_CreatesDirectory(t *testing.T) {
	base := t.TempDir()
	dir, err := plugin.PluginCacheDir(base, "ns", "name", "0.1.0")
	if err != nil {
		t.Fatalf("PluginCacheDir: %v", err)
	}
	if !strings.HasPrefix(dir, base) {
		t.Errorf("dir %q not under base %q", dir, base)
	}
}

// TestPluginCacheDirNoCreate verifies that PluginCacheDirNoCreate returns the
// expected path without creating it.
func TestPluginCacheDirNoCreate(t *testing.T) {
	base := t.TempDir()
	got := plugin.PluginCacheDirNoCreate(base, "ns", "myplugin", "1.0.0")
	want := filepath.Join(base, "plugins", "ns", "myplugin", "1.0.0", runtime.GOOS+"_"+runtime.GOARCH)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// Must NOT create the directory.
	if _, err := os.Stat(got); err == nil {
		t.Error("PluginCacheDirNoCreate must not create the directory")
	}
}

// TestBinaryName verifies the conventional binary name for a plugin.
func TestBinaryName(t *testing.T) {
	got := plugin.BinaryName("ibmbob")
	want := "llm-proxy-provider-ibmbob"
	if runtime.GOOS == "windows" {
		want += ".exe"
	}
	if got != want {
		t.Errorf("BinaryName = %q, want %q", got, want)
	}
}

// TestManifestName verifies the conventional manifest filename for a plugin.
func TestManifestName(t *testing.T) {
	got := plugin.ManifestName("ibmbob")
	want := "llm-proxy-provider-ibmbob.manifest.json"
	if got != want {
		t.Errorf("ManifestName = %q, want %q", got, want)
	}
}

// ---- InstalledVersions ------------------------------------------------------

// seedCache creates a fake installed version under the cache base and returns
// the version string that was seeded.
func seedCache(t *testing.T, base, namespace, name, version string) {
	t.Helper()
	dir, err := plugin.PluginCacheDir(base, namespace, name, version)
	if err != nil {
		t.Fatalf("PluginCacheDir: %v", err)
	}
	// Write a fake binary so InstalledVersions considers this version "present".
	binPath := filepath.Join(dir, plugin.BinaryName(name))
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
}

func TestInstalledVersions_Empty(t *testing.T) {
	base := t.TempDir()
	versions, err := plugin.InstalledVersions(base, "ns", "myplugin")
	if err != nil {
		t.Fatalf("InstalledVersions: %v", err)
	}
	if len(versions) != 0 {
		t.Errorf("expected 0, got %v", versions)
	}
}

func TestInstalledVersions_SingleVersion(t *testing.T) {
	base := t.TempDir()
	seedCache(t, base, "ns", "myplugin", "1.2.3")

	versions, err := plugin.InstalledVersions(base, "ns", "myplugin")
	if err != nil {
		t.Fatalf("InstalledVersions: %v", err)
	}
	if len(versions) != 1 || versions[0] != "1.2.3" {
		t.Errorf("got %v, want [1.2.3]", versions)
	}
}

func TestInstalledVersions_MultipleVersions(t *testing.T) {
	base := t.TempDir()
	for _, v := range []string{"1.0.0", "1.1.0", "2.0.0"} {
		seedCache(t, base, "ns", "myplugin", v)
	}

	versions, err := plugin.InstalledVersions(base, "ns", "myplugin")
	if err != nil {
		t.Fatalf("InstalledVersions: %v", err)
	}
	if len(versions) != 3 {
		t.Fatalf("got %v, want 3 versions", versions)
	}
	found := make(map[string]bool)
	for _, v := range versions {
		found[v] = true
	}
	for _, want := range []string{"1.0.0", "1.1.0", "2.0.0"} {
		if !found[want] {
			t.Errorf("version %q not found in %v", want, versions)
		}
	}
}

func TestInstalledVersions_IgnoresVersionsWithoutBinary(t *testing.T) {
	base := t.TempDir()
	// Create a version dir with no binary.
	dir, err := plugin.PluginCacheDir(base, "ns", "myplugin", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	// Write only a manifest, not the binary.
	if err := os.WriteFile(filepath.Join(dir, plugin.ManifestName("myplugin")), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Also create a complete version for comparison.
	seedCache(t, base, "ns", "myplugin", "1.1.0")

	versions, err := plugin.InstalledVersions(base, "ns", "myplugin")
	if err != nil {
		t.Fatalf("InstalledVersions: %v", err)
	}
	// Only the complete version should appear.
	if len(versions) != 1 || versions[0] != "1.1.0" {
		t.Errorf("got %v, want [1.1.0]", versions)
	}
}

func TestInstalledVersions_IgnoresOtherPlugins(t *testing.T) {
	base := t.TempDir()
	seedCache(t, base, "ns", "pluginA", "1.0.0")
	seedCache(t, base, "ns", "pluginB", "2.0.0")

	versions, err := plugin.InstalledVersions(base, "ns", "pluginA")
	if err != nil {
		t.Fatalf("InstalledVersions: %v", err)
	}
	if len(versions) != 1 || versions[0] != "1.0.0" {
		t.Errorf("got %v, want [1.0.0]", versions)
	}
}
