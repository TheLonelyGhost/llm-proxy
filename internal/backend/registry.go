package backend

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Registry is the set of Backends the server can route to, keyed by Name.
// It is safe for concurrent use.
type Registry struct {
	mu       sync.RWMutex
	backends map[string]Backend
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{backends: make(map[string]Backend)}
}

// Register adds b to the registry. Names must be unique, lowercase, non-empty,
// and may not contain a '/'.
func (r *Registry) Register(b Backend) error {
	if b == nil {
		return fmt.Errorf("nil backend")
	}
	name := b.Name()
	if name == "" {
		return fmt.Errorf("backend has empty name")
	}
	if name != strings.ToLower(name) {
		return fmt.Errorf("backend name %q must be lowercase", name)
	}
	if strings.ContainsRune(name, '/') {
		return fmt.Errorf("backend name %q must not contain '/'", name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.backends[name]; ok {
		return fmt.Errorf("backend %q already registered", name)
	}
	r.backends[name] = b
	return nil
}

// Get returns the backend registered under name, if any.
func (r *Registry) Get(name string) (Backend, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.backends[name]
	return b, ok
}

// Names returns the sorted list of registered backend names.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.backends))
	for name := range r.backends {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// All returns a snapshot of all registered backends, sorted by Name.
func (r *Registry) All() []Backend {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.backends))
	for name := range r.backends {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Backend, 0, len(names))
	for _, n := range names {
		out = append(out, r.backends[n])
	}
	return out
}
