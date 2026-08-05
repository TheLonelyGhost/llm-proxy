package backend

import "context"

// Filter applies the Model Allow-List to the set of models returned by lister
// and returns only the models whose ID appears in allowList.
//
// When allowList is empty, Filter returns (nil, nil) immediately — an empty
// allow-list means the operator has not restricted the set of models, so the
// proxy defers to the caller's handling (typically pass-through).
//
// When lister.ListUpstreamModels returns an error, Filter propagates that
// error. Callers that want a degraded-but-usable fallback should call
// StubModels on error and log the failure themselves.
func Filter(ctx context.Context, allowList []string, lister ModelLister) ([]ModelInfo, error) {
	if len(allowList) == 0 {
		return nil, nil
	}

	all, err := lister.ListUpstreamModels(ctx)
	if err != nil {
		return nil, err
	}

	allowed := make(map[string]bool, len(allowList))
	for _, m := range allowList {
		allowed[m] = true
	}

	out := make([]ModelInfo, 0, len(allowList))
	for _, m := range all {
		if allowed[m.ID] {
			out = append(out, m)
		}
	}
	return out, nil
}

// StubModels constructs a minimal []ModelInfo from allowList when a live
// upstream query is unavailable. Each entry carries only the model ID and
// the supplied vendor string. Callers use this as a degraded fallback after
// Filter returns an error.
func StubModels(allowList []string, vendor string) []ModelInfo {
	out := make([]ModelInfo, 0, len(allowList))
	for _, m := range allowList {
		out = append(out, ModelInfo{ID: m, Vendor: vendor})
	}
	return out
}
