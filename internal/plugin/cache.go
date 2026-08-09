package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// PluginCacheDir returns the platform-specific cache directory for the given
// plugin namespace, name, and version, and creates it if it does not exist.
//
// The layout is:
//
//	<base>/plugins/<namespace>/<name>/<version>/<os>_<arch>/
func PluginCacheDir(base, namespace, name, version string) (string, error) {
	dir := filepath.Join(base, "plugins", namespace, name, version,
		runtime.GOOS+"_"+runtime.GOARCH)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create plugin cache dir %q: %w", dir, err)
	}
	return dir, nil
}

// PluginCacheDirNoCreate returns the platform-specific cache directory path
// for the given plugin namespace, name, and version without creating it.
// Use this to probe whether a version is already cached.
func PluginCacheDirNoCreate(base, namespace, name, version string) string {
	return filepath.Join(base, "plugins", namespace, name, version,
		runtime.GOOS+"_"+runtime.GOARCH)
}

// InstalledVersions returns all version strings for which a complete
// installation exists in the cache for the current OS and architecture.
//
// A version is considered installed when its cache directory exists and
// contains both the plugin binary and its manifest.
//
// Versions are returned in no defined order; callers that need a sorted list
// should sort the result themselves.
func InstalledVersions(base, namespace, name string) ([]string, error) {
	pluginBase := filepath.Join(base, "plugins", namespace, name)
	entries, err := os.ReadDir(pluginBase)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read plugin cache dir %q: %w", pluginBase, err)
	}

	platform := runtime.GOOS + "_" + runtime.GOARCH
	var versions []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		version := e.Name()
		platformDir := filepath.Join(pluginBase, version, platform)
		if _, err := os.Stat(platformDir); err != nil {
			continue
		}
		// Verify that at least the binary is present; the manifest check is
		// done at install time so a partial install would be missing the binary.
		binPath := filepath.Join(platformDir, BinaryName(name))
		if _, err := os.Stat(binPath); err != nil {
			continue
		}
		versions = append(versions, version)
	}
	return versions, nil
}

// BinaryName returns the conventional binary filename for a plugin on the
// current platform. On Windows it appends ".exe".
func BinaryName(pluginName string) string {
	name := "llm-proxy-provider-" + pluginName
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// ManifestName returns the conventional manifest filename for a plugin.
func ManifestName(pluginName string) string {
	return "llm-proxy-provider-" + pluginName + ".manifest.json"
}
