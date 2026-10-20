package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"github.com/thelonelyghost/llm-proxy/internal/config"
	"github.com/thelonelyghost/llm-proxy/internal/github"
	"github.com/thelonelyghost/llm-proxy/internal/lockfile"
	"github.com/thelonelyghost/llm-proxy/internal/paths"
	"github.com/thelonelyghost/llm-proxy/internal/plugin"
	"github.com/thelonelyghost/llm-proxy/internal/semver"
)

// compiledInTypes lists the provider type strings that are compiled into the
// llm-proxy binary. Plugin block labels may not collide with these names.
var compiledInTypes = map[string]struct{}{
	"openai": {},
}

// ExitCodeError wraps a child-process exit code so that callers can
// distinguish a clean "plugin exited non-zero" from an internal proxy error
// and exit with the same code without printing a spurious error message.
type ExitCodeError struct {
	Code int
}

// Error implements the error interface.
func (e *ExitCodeError) Error() string {
	return fmt.Sprintf("exit status %d", e.Code)
}

// newPluginCmd returns the `plugin` command group.
func newPluginCmd(out, errOut io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugin",
		Short: "Manage llm-proxy sidecar plugins",
		Long:  "Subcommands for installing, updating, and running sidecar plugins declared in the configuration.",
	}
	cmd.AddCommand(newPluginInstallCmd(out))
	cmd.AddCommand(newPluginRunCmd(out, errOut))
	return cmd
}

// newPluginInstallCmd implements `llm-proxy plugin install`.
func newPluginInstallCmd(out io.Writer) *cobra.Command {
	var cfgPath string
	var upgrade bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Download and install plugins declared in the configuration",
		Long: "Reads the plugin blocks from the configuration, resolves each plugin's " +
			"version constraint (preferring the lock file, then the local cache, then the " +
			"remote registry), downloads any missing binaries into the cache directory, and " +
			"writes llm-proxy.lock.hcl. Safe to re-run: already-cached versions are skipped.\n\n" +
			"With --upgrade, the lock file and local cache are bypassed for version resolution: " +
			"the latest compatible release is always fetched from the remote registry and the " +
			"lock file is updated with the new resolved version.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(cmd, cfgPath)
			if err != nil {
				return err
			}
			if len(cfg.Plugins) == 0 {
				fmt.Fprintln(out, "No plugins declared in configuration.") //nolint:errcheck
				return nil
			}
			cacheBase, err := paths.CacheDir()
			if err != nil {
				return fmt.Errorf("resolve cache dir: %w", err)
			}

			lockPath, err := lockfile.PathFor(cfg.SourcePath)
			if err != nil {
				return fmt.Errorf("resolve lock file path: %w", err)
			}

			lf, err := readOrNewLockfile(lockPath)
			if err != nil {
				return err
			}

			for _, pb := range cfg.Plugins {
				if err := installPlugin(cmd.Context(), out, pb, cacheBase, lf, upgrade); err != nil {
					return fmt.Errorf("install plugin %q: %w", pb.Name, err)
				}
			}

			if err := lockfile.Write(lockPath, lf); err != nil {
				return fmt.Errorf("write lock file %q: %w", lockPath, err)
			}
			fmt.Fprintf(out, "Lock file written to %s\n", lockPath) //nolint:errcheck
			return nil
		},
	}
	var cfgDefault string
	if p, err := paths.ConfigFile(); err == nil {
		cfgDefault = p
	} else {
		cfgDefault = defaultConfigPath()
	}
	cmd.Flags().StringVarP(&cfgPath, "config", "c", cfgDefault,
		"path to HCL config file or directory (or set LLM_PROXY_CONFIG)")
	cmd.Flags().BoolVar(&upgrade, "upgrade", false,
		"bypass the lock file and local cache; resolve the latest compatible version from the remote registry and update the lock file")
	return cmd
}

// readOrNewLockfile reads the lock file at path, or returns an empty Lockfile
// when the file does not exist yet.
func readOrNewLockfile(path string) (*lockfile.Lockfile, error) {
	lf, err := lockfile.Read(path)
	if err == nil {
		return lf, nil
	}
	if errors.Is(err, lockfile.ErrNotFound) {
		return &lockfile.Lockfile{}, nil
	}
	return nil, fmt.Errorf("read lock file: %w", err)
}

// installPlugin resolves and (if needed) downloads a single plugin.
//
// Normal mode (upgrade=false) resolution priority:
//  1. Lock file entry for this plugin + current platform → use locked version directly.
//  2. Cache scan: find the highest already-installed version that satisfies the
//     constraint (no network call).
//  3. Remote registry (GitHub Releases API for github.com sources).
//
// Upgrade mode (upgrade=true):
//   - The lock file and cache are ignored for resolution; the latest compatible
//     version is always resolved from the remote registry.
//   - The lock file is updated with the newly resolved version.
//
// In both modes, once a concrete version is resolved, if its cache directory
// already contains the binary the download is skipped. Otherwise the archive
// is downloaded, extracted, and validated.
//
// The lock file is updated in-place; the caller writes it to disk.
func installPlugin(ctx context.Context, out io.Writer, pb config.PluginBlock, cacheBase string, lf *lockfile.Lockfile, upgrade bool) error {
	namespace, name := sourceNamespace(pb.Source, pb.Name)

	concreteVersion, source, err := resolvePluginVersion(ctx, pb, cacheBase, namespace, name, lf, upgrade)
	if err != nil {
		return err
	}

	// Cache hit: skip download if the binary already exists for this platform.
	destDir := plugin.PluginCacheDirNoCreate(cacheBase, namespace, name, concreteVersion)
	binPath := filepath.Join(destDir, plugin.BinaryName(pb.Name))
	if _, statErr := os.Stat(binPath); statErr == nil {
		fmt.Fprintf(out, "Plugin %q version %s already cached, skipping download.\n", pb.Name, concreteVersion) //nolint:errcheck
		checksum, hashErr := hashFile(binPath)
		if hashErr != nil {
			return fmt.Errorf("hash cached binary: %w", hashErr)
		}
		updateLockfile(lf, pb, concreteVersion, checksum)
		return nil
	}

	// Need to download.
	createdDestDir, err := plugin.PluginCacheDir(cacheBase, namespace, name, concreteVersion)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "Installing plugin %q version %s from %s\n", pb.Name, concreteVersion, source) //nolint:errcheck

	checksumURL := resolveChecksumURL(pb.Source, pb.ChecksumURL, concreteVersion)
	if err := fetchPluginWithChecksum(ctx, source, checksumURL, createdDestDir, pb.Name); err != nil {
		return fmt.Errorf("fetch plugin: %w", err)
	}

	// Verify the manifest exists and is valid.
	manifestPath := filepath.Join(createdDestDir, plugin.ManifestName(pb.Name))
	data, err := os.ReadFile(manifestPath) //nolint:gosec // operator-controlled cache path
	if err != nil {
		return fmt.Errorf("read manifest %q: %w", manifestPath, err)
	}
	if _, err := plugin.ParseManifest(data); err != nil {
		return fmt.Errorf("invalid manifest for plugin %q: %w", pb.Name, err)
	}

	// Make binary executable.
	finalBin := filepath.Join(createdDestDir, plugin.BinaryName(pb.Name))
	if err := os.Chmod(finalBin, 0o755); err != nil && !os.IsNotExist(err) { //nolint:gosec // intentional chmod
		return fmt.Errorf("chmod binary %q: %w", finalBin, err)
	}

	checksum, err := hashFile(finalBin)
	if err != nil {
		return fmt.Errorf("hash installed binary: %w", err)
	}

	updateLockfile(lf, pb, concreteVersion, checksum)
	fmt.Fprintf(out, "Plugin %q version %s installed successfully.\n", pb.Name, concreteVersion) //nolint:errcheck
	return nil
}

// resolvePluginVersion determines the concrete version and download source for
// a plugin block.
//
// When upgrade is false the following priority order is used:
//  1. Lock file entry present → use its resolved version (no network).
//  2. Cache scan: highest cached version satisfying the constraint (no network).
//  3. Remote registry for range constraints on github.com sources.
//  4. Exact-pin passthrough (no file I/O or network).
//
// When upgrade is true steps 1 and 2 are skipped: the remote registry is
// always consulted so that the latest compatible version is selected,
// regardless of what is already locked or cached.
func resolvePluginVersion(
	ctx context.Context,
	pb config.PluginBlock,
	cacheBase, namespace, name string,
	lf *lockfile.Lockfile,
	upgrade bool,
) (version, src string, err error) {
	if !upgrade {
		// 1. Lock file.
		if entry, ok := lf.Lookup(pb.Name); ok {
			concreteVersion := entry.Resolved
			src := resolveSource(pb.Source, pb.Name, concreteVersion)
			return concreteVersion, src, nil
		}

		// Parse the constraint once so we can reuse it below.
		c, err := semver.Parse(pb.Version)
		if err != nil {
			return "", "", fmt.Errorf("parse version constraint %q: %w", pb.Version, err)
		}

		// 2. Cache scan (only for range constraints; exact pins are authoritative).
		if !c.IsExact() {
			cached, scanErr := plugin.InstalledVersions(cacheBase, namespace, name)
			if scanErr != nil {
				return "", "", fmt.Errorf("scan cache for %q: %w", pb.Name, scanErr)
			}
			if len(cached) > 0 {
				best, matchErr := c.BestMatch(cached)
				if matchErr != nil {
					return "", "", matchErr
				}
				if best != "" {
					src := resolveSource(pb.Source, pb.Name, best)
					return best, src, nil
				}
			}
		}
	}

	// 3 / 4. Remote resolution (or exact-pin passthrough).
	// This is always reached in upgrade mode; in normal mode it is the
	// fallback when neither the lock file nor the cache produced a match.
	concreteVersion, err := resolveVersion(ctx, nil, pb.Source, pb.Version)
	if err != nil {
		return "", "", fmt.Errorf("resolve version constraint %q: %w", pb.Version, err)
	}
	src = resolveSource(pb.Source, pb.Name, concreteVersion)
	return concreteVersion, src, nil
}

// updateLockfile records the resolved version and binary checksum into lf for
// the given plugin.
func updateLockfile(lf *lockfile.Lockfile, pb config.PluginBlock, concreteVersion, checksum string) {
	lf.Set(lockfile.Entry{
		Name:       pb.Name,
		Source:     pb.Source,
		Constraint: pb.Version,
		Resolved:   concreteVersion,
		Checksum:   checksum,
	})
}

// hashFile returns the lowercase hex-encoded SHA-256 digest of the file at
// path.
func hashFile(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // operator-controlled cache path
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// resolveVersion resolves a version constraint string to a concrete version.
//
// If the constraint is an exact version pin (e.g. "1.2.3" or "= 1.2.3"),
// the pinned version is returned directly without any network call.
//
// For range constraints (e.g. "~> 1.0", ">= 1.0.0, < 2.0.0") with a
// github.com source, the GitHub Releases API is queried for all v* release
// tags and the highest matching release is returned.
//
// Range constraints on non-GitHub sources return an error.
//
// httpClient may be nil (the default HTTP client is used); tests inject a
// stub here to avoid real network calls.
func resolveVersion(ctx context.Context, httpClient *http.Client, source, versionConstraint string) (string, error) {
	c, err := semver.Parse(versionConstraint)
	if err != nil {
		return "", err
	}
	if c.IsExact() {
		return c.ExactVersion(), nil
	}

	// Range constraint — only supported for GitHub sources.
	if !strings.HasPrefix(source, "github.com/") {
		return "", fmt.Errorf(
			"version constraints (non-exact) are only supported for github.com sources, got %q",
			source,
		)
	}

	// github.com/<owner>/<repo>[/...] → "owner/repo"
	parts := strings.SplitN(strings.TrimPrefix(source, "github.com/"), "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf(
			"invalid github.com source %q: expected github.com/<owner>/<repo>",
			source,
		)
	}
	repoPath := parts[0] + "/" + parts[1]

	tags, err := github.ListReleaseTags(ctx, httpClient, repoPath)
	if err != nil {
		return "", fmt.Errorf("list releases for %s: %w", repoPath, err)
	}

	best, err := c.BestMatch(tags)
	if err != nil {
		return "", err
	}
	if best == "" {
		return "", fmt.Errorf("%w: no release matching %q found in %s",
			github.ErrNoMatchingRelease, versionConstraint, repoPath)
	}
	return best, nil
}

// fetchPluginWithChecksum downloads the plugin source into destDir.
// For file:// sources it copies the directory contents (no checksum).
// For http/https sources it downloads and extracts a tar.gz archive, and if
// checksumURL is non-empty the extracted binary is verified against SHA256SUMS.
func fetchPluginWithChecksum(ctx context.Context, src, checksumURL, destDir, pluginName string) error {
	if strings.HasPrefix(src, "file://") {
		fileDir := strings.TrimPrefix(src, "file://")
		return copyDir(fileDir, destDir)
	}
	// HTTP/HTTPS: download archive and extract.
	return downloadAndExtract(ctx, nil, src, checksumURL, destDir, pluginName)
}

// copyDir copies all files from srcDir into destDir.
func copyDir(srcDir, destDir string) error {
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return fmt.Errorf("create dest dir: %w", err)
	}
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return fmt.Errorf("read source dir %q: %w", srcDir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		srcPath := filepath.Join(srcDir, e.Name())
		dstPath := filepath.Join(destDir, e.Name())
		if err := copyFile(srcPath, dstPath); err != nil {
			return fmt.Errorf("copy %q: %w", e.Name(), err)
		}
	}
	return nil
}

// copyFile copies a single file preserving permissions.
func copyFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // operator-controlled paths
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode()) //nolint:gosec // operator-controlled paths
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	_, err = io.Copy(out, in)
	return err
}

// downloadAndExtract downloads an archive from archiveURL and extracts it into destDir.
// If checksumURL is non-empty, the SHA256SUMS file at that URL is fetched and
// the archive's SHA-256 is verified against the entry for the archive filename
// before extraction. A mismatch is a hard error.
//
// client may be nil (http.DefaultClient is used).
func downloadAndExtract(ctx context.Context, client *http.Client, archiveURL, checksumURL, destDir, _ string) error {
	if client == nil {
		client = http.DefaultClient
	}

	// Fetch the checksum map first (if requested) so we can verify the
	// archive before touching the filesystem.
	var expectedHex string
	if checksumURL != "" {
		sums, fetchErr := github.FetchChecksumFile(ctx, client, checksumURL)
		if fetchErr != nil {
			return fmt.Errorf("fetch checksum file: %w", fetchErr)
		}
		archiveName := filepath.Base(archiveURL)
		hex, ok := sums[archiveName]
		if !ok {
			return fmt.Errorf("checksum verification failed: %q not found in SHA256SUMS from %s",
				archiveName, checksumURL)
		}
		expectedHex = hex
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, archiveURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %q: %w", archiveURL, err)
	}
	if resp == nil {
		return fmt.Errorf("download %q: nil response", archiveURL)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %q: HTTP %d", archiveURL, resp.StatusCode)
	}

	// If we need to verify, buffer the archive so we can hash it.
	var archiveReader io.Reader = resp.Body
	if expectedHex != "" {
		archiveBytes, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return fmt.Errorf("read archive %q: %w", archiveURL, readErr)
		}
		h := sha256.New()
		h.Write(archiveBytes)
		actualHex := fmt.Sprintf("%x", h.Sum(nil))
		if !strings.EqualFold(actualHex, expectedHex) {
			return fmt.Errorf("checksum mismatch for %q: got %s, want %s",
				filepath.Base(archiveURL), actualHex, expectedHex)
		}
		archiveReader = bytes.NewReader(archiveBytes) // wrap bytes as reader
	}

	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return fmt.Errorf("create dest dir: %w", err)
	}
	return extractTarGz(archiveReader, destDir)
}

// resolveSource constructs a download source URL from the plugin block's
// source and version. GitHub shorthand is expanded to the Releases download URL.
// By the time this is called, version must already be a concrete semver string
// (no operators, no constraints) — see resolveVersion.
func resolveSource(source, pluginName, version string) string {
	// Handle file:// explicitly — pass through as-is.
	if strings.HasPrefix(source, "file://") {
		return source
	}

	// GitHub shorthand: github.com/<namespace>/<repo>
	if strings.HasPrefix(source, "github.com/") {
		return buildGitHubReleaseURL(source, pluginName, version)
	}

	// Explicit URL with placeholders.
	r := strings.NewReplacer(
		"{version}", version,
		"{os}", runtime.GOOS,
		"{arch}", runtime.GOARCH,
	)
	return r.Replace(source)
}

// buildGitHubReleaseURL constructs the GitHub Releases download URL for a
// plugin archive.
func buildGitHubReleaseURL(source, pluginName, version string) string {
	source = strings.TrimRight(source, "/")
	archiveName := fmt.Sprintf("%s_%s_%s_%s.tar.gz",
		plugin.BinaryName(pluginName),
		version,
		runtime.GOOS,
		runtime.GOARCH,
	)
	return fmt.Sprintf("https://%s/releases/download/v%s/%s",
		source, version, archiveName)
}

// buildGitHubChecksumURL constructs the GitHub Releases download URL for the
// SHA256SUMS file corresponding to a plugin release.
func buildGitHubChecksumURL(source, version string) string {
	source = strings.TrimRight(source, "/")
	return fmt.Sprintf("https://%s/releases/download/v%s/SHA256SUMS",
		source, version)
}

// resolveChecksumURL returns the checksum URL to use for a plugin installation.
//
// Priority:
//  1. An explicit checksumURL field from the plugin block (after placeholder
//     substitution).
//  2. Auto-derived from a github.com source.
//  3. Empty string (no verification).
func resolveChecksumURL(source, explicitChecksumURL, version string) string {
	if explicitChecksumURL != "" {
		r := strings.NewReplacer(
			"{version}", version,
			"{os}", runtime.GOOS,
			"{arch}", runtime.GOARCH,
		)
		return r.Replace(explicitChecksumURL)
	}
	if strings.HasPrefix(source, "github.com/") {
		return buildGitHubChecksumURL(source, version)
	}
	return ""
}

// sourceNamespace returns the (namespace, name) pair used for cache directory
// layout. For GitHub sources it extracts from the URL; otherwise it uses the
// plugin block name.
func sourceNamespace(source, pluginName string) (namespace, name string) {
	if strings.HasPrefix(source, "github.com/") {
		// github.com/<namespace>/<repo> → namespace=<namespace>, name=<pluginName>
		parts := strings.SplitN(strings.TrimPrefix(source, "github.com/"), "/", 2)
		if len(parts) >= 1 && parts[0] != "" {
			return parts[0], pluginName
		}
	}
	return pluginName, pluginName
}

// newPluginRunCmd implements `llm-proxy plugin run <backend-label> <subcommand> [args...]`.
func newPluginRunCmd(out, errOut io.Writer) *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "run <backend-label> <subcommand> [args...]",
		Short: "Run a tooling subcommand provided by an installed plugin",
		Long: "Resolves <backend-label> to a configured backend, finds the plugin that " +
			"backs it, and invokes the installed plugin binary in tooling mode, passing " +
			"the subcommand name via --tool and forwarding stdin/stdout/stderr. " +
			"The backend configuration is forwarded to the plugin via environment " +
			"variables (LLM_PROXY_BACKEND_LABEL, LLM_PROXY_BACKEND_TYPE, " +
			"LLM_PROXY_BACKEND_CONFIG). " +
			"Exits with the plugin's exit code.",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			backendLabel := args[0]
			subcommand := args[1]
			rest := args[2:]

			cfg, err := loadConfig(cmd, cfgPath)
			if err != nil {
				return err
			}

			// Resolve the backend by label.
			var bb *config.BackendBlock
			for i := range cfg.Backends {
				if cfg.Backends[i].Name == backendLabel {
					bb = &cfg.Backends[i]
					break
				}
			}
			if bb == nil {
				return fmt.Errorf("no backend %q declared in configuration", backendLabel)
			}

			// The backend's type must be a plugin (not a compiled-in provider).
			if _, isCompiled := compiledInTypes[bb.Type]; isCompiled {
				return fmt.Errorf("backend %q uses compiled-in provider type %q which does not support plugin tooling", backendLabel, bb.Type)
			}

			// Find the plugin block whose label matches bb.Type.
			var pb *config.PluginBlock
			for i := range cfg.Plugins {
				if cfg.Plugins[i].Name == bb.Type {
					pb = &cfg.Plugins[i]
					break
				}
			}
			if pb == nil {
				return fmt.Errorf("no plugin %q declared in configuration (required by backend %q)", bb.Type, backendLabel)
			}

			cacheBase, err := paths.CacheDir()
			if err != nil {
				return fmt.Errorf("resolve cache dir: %w", err)
			}

			// For `plugin run`, prefer the lock file for version resolution so
			// we can locate the installed binary without a network round-trip.
			lockPath, _ := lockfile.PathFor(cfg.SourcePath)
			lf, _ := readOrNewLockfile(lockPath)

			namespace, name := sourceNamespace(pb.Source, pb.Name)
			concreteVersion, _, err := resolvePluginVersion(cmd.Context(), *pb, cacheBase, namespace, name, lf, false)
			if err != nil {
				return fmt.Errorf("resolve version for plugin %q: %w", pb.Name, err)
			}

			destDir := plugin.PluginCacheDirNoCreate(cacheBase, namespace, name, concreteVersion)
			binPath := filepath.Join(destDir, plugin.BinaryName(pb.Name))

			// Build backend-context environment variables for the plugin.
			extraEnv, err := buildBackendEnv(*bb)
			if err != nil {
				return fmt.Errorf("build backend env for %q: %w", backendLabel, err)
			}

			toolArgs := append([]string{"--tool", subcommand}, rest...)
			err = execPlugin(cmd.Context(), out, errOut, binPath, toolArgs, extraEnv)
			if err != nil {
				// Suppress cobra's own error message for ExitCodeError: the
				// plugin process already wrote its own diagnostics to stderr.
				var ece *ExitCodeError
				if errors.As(err, &ece) {
					cmd.SilenceErrors = true
				}
			}
			return err
		},
	}
	var cfgDefault string
	if p, err := paths.ConfigFile(); err == nil {
		cfgDefault = p
	} else {
		cfgDefault = defaultConfigPath()
	}
	cmd.Flags().StringVarP(&cfgPath, "config", "c", cfgDefault,
		"path to HCL config file or directory (or set LLM_PROXY_CONFIG)")
	// Stop flag parsing after the first non-flag argument so that flags
	// intended for the plugin binary (e.g. --verbose) are forwarded as-is.
	cmd.Flags().SetInterspersed(false)
	return cmd
}

// buildBackendEnv constructs the extra environment variables that are
// forwarded to the plugin process, both for `plugin run` tool invocations and
// for the sidecar launched by `serve`. It sets:
//
//	LLM_PROXY_BACKEND_LABEL  – the backend block label (e.g. "ollama")
//	LLM_PROXY_BACKEND_TYPE   – the plugin type (e.g. "copilot")
//	LLM_PROXY_BACKEND_CONFIG – JSON object with all backend attributes
func buildBackendEnv(bb config.BackendBlock) ([]string, error) {
	configJSON, err := config.BackendConfigJSON(bb)
	if err != nil {
		return nil, err
	}
	return []string{
		"LLM_PROXY_BACKEND_LABEL=" + bb.Name,
		"LLM_PROXY_BACKEND_TYPE=" + bb.Type,
		"LLM_PROXY_BACKEND_CONFIG=" + configJSON,
	}, nil
}

// extraEnv is appended to the current process environment before exec.
func execPlugin(ctx context.Context, _ io.Writer, _ io.Writer, binPath string, args []string, extraEnv []string) error {
	// Validate the path is absolute to avoid path injection.
	if !filepath.IsAbs(binPath) {
		return fmt.Errorf("plugin binary path must be absolute: %q", binPath)
	}
	if _, err := os.Stat(binPath); err != nil {
		return fmt.Errorf("plugin binary not found at %q (run `llm-proxy plugin install` first)", binPath)
	}

	argv := append([]string{binPath}, args...)
	_ = argv // used below via syscall exec on unix / cmd.Run on all platforms

	if err := execPluginProcess(ctx, binPath, args, extraEnv); err != nil {
		// Translate exec.ExitError into ExitCodeError so callers can exit
		// with the plugin's exact code without re-printing the error.
		var exitErr interface{ ExitCode() int }
		if errors.As(err, &exitErr) {
			return &ExitCodeError{Code: exitErr.ExitCode()}
		}
		return err
	}
	return nil
}
