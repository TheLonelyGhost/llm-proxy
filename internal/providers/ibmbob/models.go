package ibmbob

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
)

// modelsPathAuthn / modelsPathLegacy are the upstream catalogue routes for
// the modern (authn) and legacy Bob backends respectively. They mirror the
// chat-completions path layout. The /model/info surface is richer than the
// stock OpenAI /models route - it carries provider-specific metadata
// (context window, pricing, deprecation, etc.) per model.
const (
	modelsPathAuthn  = "/inference/v1/model/info"
	modelsPathLegacy = "/v1/model/info"
)

// rawBobModel decodes the documented OpenAI-compatible /v1/model/info entry
// plus any free-form fields the Bob backend chooses to add. Unknown fields
// are captured in extras so the CLI can surface them under --verbose
// without the proxy needing to track schema churn.
//
// The /v1/model/info surface (a LiteLLM convention) keys each entry by
// `model_name` rather than the OpenAI `id`, and nests per-model metadata
// under a `model_info` object. We accept both shapes so the decoder stays
// compatible if upstream switches back to the plain /v1/models layout.
type rawBobModel struct {
	ID        string `json:"id"`
	ModelName string `json:"model_name"`
	Object    string `json:"object"`
	OwnedBy   string `json:"owned_by"`
	Created   int64  `json:"created"`
}

type rawBobModelsResponse struct {
	Object string            `json:"object"`
	Data   []json.RawMessage `json:"data"`
}

// ListUpstreamModels queries the Bob backend's /v1/model/info catalogue and
// returns a normalised list. The route depends on whether the configured
// api_key targets the authn or legacy backend; identity is resolved first
// so the request carries the correct instance/team headers.
//
// /v1/model/info follows the LiteLLM convention: each entry is keyed by
// `model_name` and carries provider-specific metadata under a nested
// `model_info` object (context window, deprecation, pricing hints, etc.).
// We accept the OpenAI /v1/models shape as well so the decoder is resilient
// to upstream surface changes.
//
// This implements the optional backend.ModelLister interface.
func (p *Provider) ListUpstreamModels(ctx context.Context) ([]backend.ModelInfo, error) {
	if err := p.ensureIdentity(ctx); err != nil {
		return nil, err
	}
	path := modelsPathLegacy
	if p.authnBackend {
		path = modelsPathAuthn
	}
	resp, err := p.fetchModels(ctx, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read ibmbob models response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("ibmbob %s returned HTTP %d: %s",
			path, resp.StatusCode, truncate(string(body), 200))
	}
	var parsed rawBobModelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode ibmbob models: %w", err)
	}
	out := make([]backend.ModelInfo, 0, len(parsed.Data))
	for _, entry := range parsed.Data {
		info, ok := decodeBobModelEntry(entry)
		if !ok {
			continue
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// fetchModels issues a single GET to the upstream catalogue route with all
// required headers (including HMAC signing when needed). Unlike forward,
// it does not retry on transient markers - the listing is a one-shot
// human-facing query.
func (p *Provider) fetchModels(ctx context.Context, path string) (*http.Response, error) {
	target, pathAndQuery, err := p.buildURL(path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build models request: %w", err)
	}
	p.setHeaders(req, pathAndQuery, nil)
	// GET has no body; clear Content-Type to avoid confusing strict
	// upstream parsers.
	req.Header.Del("Content-Type")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ibmbob list models: %w", err)
	}
	if resp == nil {
		return nil, fmt.Errorf("ibmbob list models: nil response")
	}
	return resp, nil
}

// decodeBobModelEntry parses one /v1/model/info data element into ModelInfo,
// returning false if the entry has no usable id. The model identifier is
// taken from `model_name` (the LiteLLM convention used by /v1/model/info)
// or falls back to `id` for the plain /v1/models layout. Documented OpenAI
// fields are promoted to first-class ModelInfo fields; everything else,
// including the nested `model_info` block, is stashed in Extras with stable
// string keys so callers can render them verbatim.
func decodeBobModelEntry(entry json.RawMessage) (backend.ModelInfo, bool) {
	var typed rawBobModel
	if err := json.Unmarshal(entry, &typed); err != nil {
		return backend.ModelInfo{}, false
	}
	id := typed.ModelName
	if id == "" {
		id = typed.ID
	}
	if id == "" {
		return backend.ModelInfo{}, false
	}
	info := backend.ModelInfo{
		ID:     id,
		Vendor: typed.OwnedBy,
		Extras: map[string]string{},
	}
	if typed.Created > 0 {
		info.Extras["created"] = strconv.FormatInt(typed.Created, 10)
	}

	// Decode the raw entry a second time as a generic map so any
	// provider-specific extras (description, family, max_tokens,
	// nested model_info object, ...) land in info.Extras without
	// requiring schema updates here.
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(entry, &generic); err == nil {
		for k, v := range generic {
			switch k {
			case "id", "model_name", "object", "owned_by", "created":
				continue
			case "model_info":
				flattenModelInfoBlock(v, info.Extras)
				continue
			}
			info.Extras[k] = flattenExtraValue(v)
		}
	}

	if len(info.Extras) == 0 {
		info.Extras = nil
	}
	return info, true
}

// flattenModelInfoBlock unpacks the nested `model_info` object that
// /v1/model/info uses to carry per-model metadata. Each child key lands in
// extras under an `info_` prefix so it cannot collide with top-level
// fields, and the rendering remains stable across upstream additions.
func flattenModelInfoBlock(raw json.RawMessage, extras map[string]string) {
	var nested map[string]json.RawMessage
	if err := json.Unmarshal(raw, &nested); err != nil {
		extras["model_info"] = flattenExtraValue(raw)
		return
	}
	for k, v := range nested {
		extras["info_"+k] = flattenExtraValue(v)
	}
}

// flattenExtraValue renders a JSON value as a short, human-readable string
// suitable for a CLI extras map. Strings unwrap their quotes; everything
// else is left as JSON.
func flattenExtraValue(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}
