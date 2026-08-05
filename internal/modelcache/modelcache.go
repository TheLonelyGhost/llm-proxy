// Package modelcache provides a two-layer (in-memory + on-disk) cache for the
// unfiltered upstream model catalogue of each backend.
//
// The cache is keyed by Backend Name. On-disk files are stored under
// paths.CacheDir() as "models-<name>.json". In-memory state is protected by a
// mutex and shared across all goroutines within the same process.
//
// The cache stores [backend.ModelInfo] values as returned directly by the
// upstream, before the Model Allow-List is applied. This means adding or
// removing models from the allow-list takes effect immediately without any
// cache invalidation.
//
// Cache entries expire after the configured TTL, measured from the fetched_at
// timestamp embedded in the on-disk file. When both the in-memory copy and the
// disk file are stale (or absent) and the upstream call fails, the stale data
// is returned with a WARN-level log rather than propagating the error.
//
// Writes are atomic: data is written to a sibling temp file and renamed into
// place so readers never see a partial write.
package modelcache

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
	"github.com/thelonelyghost/llm-proxy/internal/paths"
)

// DefaultTTL is the recommended TTL for model catalogue caches (24 hours).
const DefaultTTL = 24 * time.Hour

// entry is the JSON envelope stored on disk.
type entry struct {
	FetchedAt time.Time           `json:"fetched_at"`
	Models    []backend.ModelInfo `json:"models"`
}

// state holds the in-memory layer, protected by a mutex.
type state struct {
	mu        sync.Mutex
	cached    []backend.ModelInfo
	fetchedAt time.Time // zero means "no valid in-memory copy"
}

// WrappedFunc is the result of [NewWrappedFunc]. Providers store one instance
// per backend, call [WrappedFunc.Call] from their Models() method, and call
// [WrappedFunc.Invalidate] from their Reload() method.
type WrappedFunc struct {
	s    *state
	call func(context.Context) ([]backend.ModelInfo, error)
}

// Call returns the cached upstream model list, fetching from upstream when
// the cache is absent or stale. It is safe for concurrent use.
func (w *WrappedFunc) Call(ctx context.Context) ([]backend.ModelInfo, error) {
	return w.call(ctx)
}

// ListUpstreamModels satisfies [backend.ModelLister] so that a [WrappedFunc]
// can be passed directly to [backend.AllowList.Models].
func (w *WrappedFunc) ListUpstreamModels(ctx context.Context) ([]backend.ModelInfo, error) {
	return w.call(ctx)
}

// Invalidate clears the in-memory copy so the next [WrappedFunc.Call]
// performs a fresh upstream fetch regardless of the disk file age. The
// on-disk file is not removed; it is overwritten on the next successful
// upstream fetch.
func (w *WrappedFunc) Invalidate() {
	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	w.s.cached = nil
	w.s.fetchedAt = time.Time{}
}

// NewWrappedFunc constructs a [WrappedFunc] for a single backend.
//
//   - name is the Backend Name; it determines the on-disk filename
//     ("models-<name>.json" under paths.CacheDir()).
//   - ttl is the maximum age of a cache entry before it is considered stale;
//     use [DefaultTTL] for the standard 24-hour window.
//   - logger may be nil, in which case slog.Default() is used.
//   - fetch is the upstream call; it must return the unfiltered model list.
func NewWrappedFunc(
	name string,
	ttl time.Duration,
	logger *slog.Logger,
	fetch func(context.Context) ([]backend.ModelInfo, error),
) *WrappedFunc {
	if logger == nil {
		logger = slog.Default()
	}
	s := &state{}
	return &WrappedFunc{s: s, call: buildCall(name, ttl, logger, fetch, s)}
}

// buildCall is the shared implementation of the cached-fetch logic.
func buildCall(
	name string,
	ttl time.Duration,
	logger *slog.Logger,
	fetch func(context.Context) ([]backend.ModelInfo, error),
	s *state,
) func(context.Context) ([]backend.ModelInfo, error) {
	return func(ctx context.Context) ([]backend.ModelInfo, error) {
		now := time.Now()

		s.mu.Lock()
		defer s.mu.Unlock()

		// 1. In-memory hit: fresh copy already loaded this process lifetime.
		if !s.fetchedAt.IsZero() && now.Sub(s.fetchedAt) < ttl {
			return s.cached, nil
		}

		// 2. Disk hit: file exists and is within TTL (e.g. after a restart).
		diskEntry, diskErr := loadDisk(name)
		if diskErr == nil && now.Sub(diskEntry.FetchedAt) < ttl {
			s.cached = diskEntry.Models
			s.fetchedAt = diskEntry.FetchedAt
			return s.cached, nil
		}

		// 3. Cache miss or stale: attempt upstream fetch.
		models, fetchErr := fetch(ctx)
		if fetchErr == nil {
			e := entry{FetchedAt: now, Models: models}
			if writeErr := writeDisk(name, e); writeErr != nil {
				logger.Warn("model cache write failed",
					"backend", name,
					"err", writeErr,
				)
			}
			s.cached = models
			s.fetchedAt = now
			return models, nil
		}

		// 4. Upstream failed — serve stale data with a warning, if any exists.
		logger.Warn("upstream model fetch failed; serving stale cache",
			"backend", name,
			"err", fetchErr,
		)

		// Prefer the in-memory stale copy (avoids redundant disk I/O).
		if s.cached != nil {
			return s.cached, nil
		}
		// Fall back to the on-disk stale copy.
		if diskErr == nil && diskEntry.Models != nil {
			s.cached = diskEntry.Models
			s.fetchedAt = diskEntry.FetchedAt
			return s.cached, nil
		}

		// No cached data at all: propagate the upstream error.
		return nil, fmt.Errorf("model cache: upstream fetch failed and no cached data available: %w", fetchErr)
	}
}

// cacheFilePath returns the path to the on-disk cache file for name.
func cacheFilePath(name string) (string, error) {
	dir, err := paths.CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "models-"+name+".json"), nil
}

// loadDisk reads and decodes the on-disk cache file for name.
// Returns a non-nil error when the file is absent, unreadable, or malformed.
func loadDisk(name string) (entry, error) {
	path, err := cacheFilePath(name)
	if err != nil {
		return entry{}, err
	}
	data, err := os.ReadFile(path) //nolint:gosec // proxy-internal cache paths under CacheDir()
	if err != nil {
		return entry{}, err
	}
	var e entry
	if err := json.Unmarshal(data, &e); err != nil {
		return entry{}, fmt.Errorf("decode model cache %q: %w", path, err)
	}
	return e, nil
}

// writeDisk writes e to the on-disk cache file for name using an atomic
// temp-file rename. Files are created with mode 0644; model catalogue data
// is not sensitive so the restrictive 0600 used for Credentials is not needed.
func writeDisk(name string, e entry) error {
	path, err := cacheFilePath(name)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return fmt.Errorf("encode model cache: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create cache dir %q: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-models-*")
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
	if err := tmp.Chmod(0o644); err != nil { //nolint:gosec // 0644 intentional: model catalogue is not sensitive
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
