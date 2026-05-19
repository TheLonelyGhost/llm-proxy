package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
)

// modelsPath is the upstream catalogue route the Copilot editor extensions
// hit to discover available models.
const modelsPath = "/models"

// rawModel mirrors the subset of fields the Copilot /models endpoint
// returns. Only fields the CLI surfaces are decoded; everything else is
// ignored so upstream schema drift does not break the listing.
type rawModel struct {
	ID                 string          `json:"id"`
	Name               string          `json:"name"`
	Object             string          `json:"object"`
	Vendor             string          `json:"vendor"`
	Version            string          `json:"version"`
	Preview            bool            `json:"preview"`
	ModelPickerEnabled bool            `json:"model_picker_enabled"`
	Capabilities       rawCapabilities `json:"capabilities"`
	Policy             *rawPolicy      `json:"policy"`
}

type rawCapabilities struct {
	Type      string                     `json:"type"`
	Family    string                     `json:"family"`
	Tokenizer string                     `json:"tokenizer"`
	Limits    map[string]json.RawMessage `json:"limits"`
	Supports  map[string]json.RawMessage `json:"supports"`
	Object    string                     `json:"object"`
}

type rawPolicy struct {
	State string `json:"state"`
	Terms string `json:"terms"`
}

type rawModelsResponse struct {
	Data []rawModel `json:"data"`
}

// ListUpstreamModels queries the Copilot /models catalogue with the proxy's
// stored credentials and returns a normalised list. Models whose policy
// state is explicitly "disabled" are filtered out; models with no policy
// field, or any other state, are included.
//
// This implements the optional backend.ModelLister interface.
func (p *Provider) ListUpstreamModels(ctx context.Context) ([]backend.ModelInfo, error) {
	resp, err := p.doOnce(ctx, http.MethodGet, modelsPath, nil)
	if err != nil {
		return nil, fmt.Errorf("copilot list models: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		// Drain, invalidate the cached short-lived token, then retry
		// once - mirrors the chat path.
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		p.auth.Invalidate()
		resp, err = p.doOnce(ctx, http.MethodGet, modelsPath, nil)
		if err != nil {
			return nil, fmt.Errorf("copilot list models (retry): %w", err)
		}
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read copilot models response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("copilot /models returned HTTP %d: %s",
			resp.StatusCode, truncate(string(body), 200))
	}
	var parsed rawModelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode copilot models: %w", err)
	}
	out := make([]backend.ModelInfo, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if m.ID == "" {
			continue
		}
		if m.Policy != nil && strings.EqualFold(m.Policy.State, "disabled") {
			continue
		}
		out = append(out, convertCopilotModel(m))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// convertCopilotModel maps a raw upstream entry into backend.ModelInfo.
// Supplemental fields land in Extras with stable lowercase keys so the CLI
// can render them deterministically.
func convertCopilotModel(m rawModel) backend.ModelInfo {
	caps := make([]string, 0, 4)
	if m.Capabilities.Type != "" {
		caps = append(caps, m.Capabilities.Type)
	}
	for k, v := range m.Capabilities.Supports {
		var enabled bool
		if err := json.Unmarshal(v, &enabled); err == nil {
			if enabled {
				caps = append(caps, k)
			}
			continue
		}
		// Non-boolean values (Copilot occasionally reports numeric or
		// enum-shaped support hints) still indicate the capability is
		// present in some form; include the key so it surfaces.
		caps = append(caps, k)
	}
	sort.Strings(caps)

	extras := map[string]string{}
	if m.Capabilities.Family != "" {
		extras["family"] = m.Capabilities.Family
	}
	if m.Capabilities.Tokenizer != "" {
		extras["tokenizer"] = m.Capabilities.Tokenizer
	}
	if m.Preview {
		extras["preview"] = "true"
	}
	if m.ModelPickerEnabled {
		extras["model_picker_enabled"] = "true"
	}
	if m.Policy != nil && m.Policy.State != "" {
		extras["policy_state"] = m.Policy.State
	}
	for k, v := range m.Capabilities.Limits {
		extras["limit_"+k] = flattenCopilotLimit(v)
	}
	if len(extras) == 0 {
		extras = nil
	}

	return backend.ModelInfo{
		ID:           m.ID,
		Name:         m.Name,
		Vendor:       m.Vendor,
		Version:      m.Version,
		Capabilities: caps,
		Extras:       extras,
	}
}

// flattenCopilotLimit renders one capabilities.limits value as a string.
// Most fields are plain integers (e.g. max_prompt_tokens), but Copilot also
// emits nested objects (e.g. max_inputs.{prompt,vision}) for some models;
// strings are unwrapped, everything else is left as compact JSON so the
// CLI can still show it under --verbose without losing information.
func flattenCopilotLimit(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}
