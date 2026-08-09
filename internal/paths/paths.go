// Package paths resolves platform-appropriate locations for proxy state.
//
// Long-lived credentials are written under the user's config dir, while
// short-lived caches (e.g., exchanged Copilot API tokens) live under the
// user's cache dir. Both directories are created on demand with 0700
// permissions to limit exposure.
package paths

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
)

// AppName is the directory name used under both config and cache roots.
const AppName = "llm-proxy"

// ConfigFile returns the persistent configuration file for the proxy.
//
// On Linux this is typically ~/.config/llm-proxy/config.hcl, on macOS
// ~/Library/Application Support/llm-proxy/config.hcl, and on Windows
// %AppData%\llm-proxy\config.hcl. The parent directory is created (mode
// 0700) if missing.
func ConfigFile() (string, error) {
	if dir, ok := os.LookupEnv("LLM_PROXY_CONFIG"); ok {
		return dir, nil
	}
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.hcl"), nil
}

// ConfigDir returns the persistent configuration directory for the proxy.
//
// On Linux this is typically ~/.config/llm-proxy, on macOS
// ~/Library/Application Support/llm-proxy, and on Windows
// %AppData%\llm-proxy. The directory is created (mode 0700) if missing.
func ConfigDir() (string, error) {
	if dir, ok := os.LookupEnv("LLM_PROXY_CONFIG_DIR"); ok {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("create config dir %q: %w", dir, err)
		}
		return dir, nil
	}
	return configDirForGOOS(runtime.GOOS)
}

func configDirForGOOS(goos string) (string, error) {
	if u, _ := user.Current(); goos != "windows" && u != nil && u.Uid == "0" {
		return "/etc/" + AppName, nil
	}
	if goos == "darwin" {
		if legacy, ok := darwinLegacyConfigDir(); ok {
			return legacy, nil
		}
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config dir: %w", err)
	}
	dir := filepath.Join(base, AppName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create config dir %q: %w", dir, err)
	}
	return dir, nil
}

func darwinLegacyConfigDir() (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", false
	}
	legacy := filepath.Join(home, ".config", AppName)
	if st, err := os.Stat(legacy); err == nil && st.IsDir() { //nolint:gosec // deliberate existence check for user-owned config path
		return legacy, true
	}
	return "", false
}

// CacheDir returns the cache directory used for short-lived state such as
// exchanged provider tokens. The directory is created (mode 0700) if missing.
func CacheDir() (string, error) {
	if dir, ok := os.LookupEnv("LLM_PROXY_CACHE_DIR"); ok {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("create cache dir %q: %w", dir, err)
		}
		return dir, nil
	}
	return cacheDirForGOOS(runtime.GOOS)
}

func cacheDirForGOOS(goos string) (string, error) {
	if goos == "darwin" {
		// Prefer XDG_CACHE_HOME when set, then $HOME/.cache, so that users
		// who expect XDG conventions on macOS get ~/.cache/llm-proxy/plugins
		// rather than ~/Library/Caches/llm-proxy/plugins.
		if xdg, ok := os.LookupEnv("XDG_CACHE_HOME"); ok && filepath.IsAbs(xdg) {
			dir := filepath.Join(xdg, AppName)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return "", fmt.Errorf("create cache dir %q: %w", dir, err)
			}
			return dir, nil
		}
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			dir := filepath.Join(home, ".cache", AppName)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return "", fmt.Errorf("create cache dir %q: %w", dir, err)
			}
			return dir, nil
		}
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve user cache dir: %w", err)
	}
	dir := filepath.Join(base, AppName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create cache dir %q: %w", dir, err)
	}
	return dir, nil
}

// WriteSecret atomically writes data to path with mode 0600.
// It writes to a sibling temp file first, then renames into place.
func WriteSecret(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create dir %q: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	cleanup = false
	return nil
}
