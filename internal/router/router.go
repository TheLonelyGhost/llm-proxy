// Package router implements prefix-based model routing.
//
// Clients address backends through the model id they send. A model id of the
// form "<backend>/<canonical-model>" routes to the named backend with the
// canonical model id stripped of the prefix. For example:
//
//	"copilot/gpt-4o" -> backend "copilot", model "gpt-4o"
//
// Empty or prefix-less ids are rejected; the proxy refuses to silently pick a
// default backend to avoid accidental traffic routing.
package router

import (
	"errors"
	"strings"
)

// ErrInvalidModelID is returned when a model id cannot be parsed.
var ErrInvalidModelID = errors.New("invalid model id: expected '<backend>/<model>'")

// Route is the parsed result of a model id.
type Route struct {
	Backend string // e.g. "copilot"
	Model   string // e.g. "gpt-4o"
}

// Parse splits a client-supplied model id into its backend prefix and the
// canonical model name expected by that backend.
//
// The split is made at the first '/' only; any subsequent '/' characters are
// preserved as part of the model name and forwarded to the upstream backend
// verbatim. Both halves must be non-empty after trimming surrounding
// whitespace. The backend half is lowercased to match registry keys.
func Parse(id string) (Route, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Route{}, ErrInvalidModelID
	}
	idx := strings.IndexByte(id, '/')
	if idx <= 0 || idx == len(id)-1 {
		return Route{}, ErrInvalidModelID
	}
	left := id[:idx]
	right := id[idx+1:]
	if strings.TrimSpace(left) != left || strings.TrimSpace(right) != right {
		return Route{}, ErrInvalidModelID
	}
	return Route{Backend: strings.ToLower(left), Model: right}, nil
}

// Format reverses Parse, producing the id form used by clients.
func Format(backend, model string) string {
	return backend + "/" + model
}
