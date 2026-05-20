package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigAndCacheDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmp, "cache"))
	// macOS/Windows ignore XDG; ensure HOME is also redirected so
	// os.UserConfigDir/UserCacheDir resolve under tmp.
	t.Setenv("HOME", tmp)
	t.Setenv("APPDATA", filepath.Join(tmp, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(tmp, "AppData", "Local"))

	cfg, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir: %v", err)
	}
	if filepath.Base(cfg) != AppName {
		t.Errorf("expected config dir to end in %q, got %q", AppName, cfg)
	}
	if st, err := os.Stat(cfg); err != nil || !st.IsDir() {
		t.Errorf("config dir not created: %v", err)
	}

	cache, err := CacheDir()
	if err != nil {
		t.Fatalf("CacheDir: %v", err)
	}
	if filepath.Base(cache) != AppName {
		t.Errorf("expected cache dir to end in %q, got %q", AppName, cache)
	}
}

func TestConfigDir_DarwinPrefersLegacyPath(t *testing.T) {
	tmp := t.TempDir()
	legacy := filepath.Join(tmp, ".config", AppName)
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	if got, err := configDirForGOOS("darwin"); err != nil {
		t.Fatalf("configDirForGOOS: %v", err)
	} else if got != legacy {
		t.Fatalf("got %q, want %q", got, legacy)
	}
}

func TestConfigDir_DarwinFallsBackToApplePath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	got, err := configDirForGOOS("darwin")
	if err != nil {
		t.Fatalf("configDirForGOOS: %v", err)
	}
	want := filepath.Join(tmp, "Library", "Application Support", AppName)
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if st, err := os.Stat(got); err != nil || !st.IsDir() {
		t.Fatalf("apple config dir not created: %v", err)
	}
}

func TestWriteSecret(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "secret.json")
	if err := WriteSecret(p, []byte(`{"k":"v"}`)); err != nil {
		t.Fatalf("WriteSecret: %v", err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	b, err := os.ReadFile(p) //nolint:gosec
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(b) != `{"k":"v"}` {
		t.Errorf("content = %q", string(b))
	}

	// Overwrite path - must succeed atomically.
	if err := WriteSecret(p, []byte("x")); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
}

func TestWriteSecret_BadDir(t *testing.T) {
	// Pass a path whose parent already exists as a file -> MkdirAll fails.
	dir := t.TempDir()
	conflict := filepath.Join(dir, "file")
	if err := os.WriteFile(conflict, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := WriteSecret(filepath.Join(conflict, "child", "secret"), []byte("x"))
	if err == nil {
		t.Fatal("expected error")
	}
}
