package cli

// This file exposes internal symbols for white-box testing.
// It is compiled only during `go test` runs.

import (
	"context"
	"net/http"

	"github.com/thelonelyghost/llm-proxy/internal/config"
	"github.com/thelonelyghost/llm-proxy/internal/lockfile"
)

// ResolveVersionForTest is the exported wrapper for resolveVersion used by
// package-level tests so they can inject a custom HTTP client.
func ResolveVersionForTest(ctx context.Context, client *http.Client, source, versionConstraint string) (string, error) {
	return resolveVersion(ctx, client, source, versionConstraint)
}

// ResolvePluginVersionForTest is the exported wrapper for resolvePluginVersion
// used by tests that need to exercise the full priority chain
// (lockfile → cache → remote) with injected data.
func ResolvePluginVersionForTest(
	ctx context.Context,
	pb config.PluginBlock,
	cacheBase, namespace, name string,
	lf *lockfile.Lockfile,
	upgrade bool,
) (version, src string, err error) {
	return resolvePluginVersion(ctx, pb, cacheBase, namespace, name, lf, upgrade)
}

// ReadOrNewLockfileForTest exposes readOrNewLockfile for tests.
func ReadOrNewLockfileForTest(path string) (*lockfile.Lockfile, error) {
	return readOrNewLockfile(path)
}
