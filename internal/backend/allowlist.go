package backend

import (
	"context"
	"fmt"
)

// AllowList enforces the Model Allow-List for a backend. It owns the
// Allowed gate (is this model permitted?) and the Models query (list the
// permitted models, with a stub fallback on upstream failure).
//
// Callers construct one AllowList per provider instance and delegate their
// Allowed() guard and Models() implementation to it. An empty allow-list
// means pass-through: all models are permitted and Models() returns nil
// without consulting the upstream.
type AllowList struct {
	list []string
}

// NewAllowList constructs an AllowList from a slice of Canonical Model IDs.
// A nil or empty slice means "no restriction" (pass-through mode).
func NewAllowList(list []string) AllowList {
	return AllowList{list: list}
}

// Allowed returns nil when model is permitted, or an ErrUnknownModel-wrapped
// error when it is not. An empty allow-list always returns nil.
func (al AllowList) Allowed(model string) error {
	if len(al.list) == 0 {
		return nil
	}
	for _, m := range al.list {
		if m == model {
			return nil
		}
	}
	return fmt.Errorf("%w: %q not in backend allow-list", ErrUnknownModel, model)
}

// Models returns the filtered, upstream-enriched model list. When the
// allow-list is empty, (nil, nil) is returned immediately without calling
// lister. When lister.ListUpstreamModels fails, Models falls back to a stub
// slice (one entry per allow-list entry with only the ID set) rather than
// propagating the error, mirroring the degraded-but-usable fallback that
// all providers apply.
func (al AllowList) Models(ctx context.Context, lister ModelLister) ([]ModelInfo, error) {
	if len(al.list) == 0 {
		return nil, nil
	}
	filtered, err := Filter(ctx, al.list, lister)
	if err != nil {
		return StubModels(al.list, ""), nil //nolint:nilerr // deliberate: stub fallback swallows the upstream error
	}
	return filtered, nil
}
