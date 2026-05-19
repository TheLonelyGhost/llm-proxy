package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
)

type rawModelListResponse struct {
	Object string          `json:"object"`
	Data   []rawModelEntry `json:"data"`
}

type rawModelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// forward dispatches an upstream HTTP request to the OpenAI-compatible API.
// The returned Response.Body is owned by the caller and must be closed.
func (p *Provider) forward(ctx context.Context, method, path string, body []byte, _ bool) (*backend.Response, error) {
	url := p.settings.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if p.settings.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.settings.APIKey)
	}

	resp, err := p.client.Do(req) //nolint:bodyclose // caller closes via backend.Response.Body
	if err != nil {
		return nil, fmt.Errorf("openai request: %w", err)
	}
	if resp == nil {
		return nil, fmt.Errorf("nil response from HTTP client")
	}

	return &backend.Response{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       resp.Body,
	}, nil
}

// ListUpstreamModels queries the upstream /v1/models endpoint.
func (p *Provider) ListUpstreamModels(ctx context.Context) ([]backend.ModelInfo, error) {
	url := p.settings.BaseURL + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build models request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	if p.settings.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.settings.APIKey)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list models request: %w", err)
	}
	if resp == nil {
		return nil, fmt.Errorf("nil response from HTTP client")
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list models failed with status %d: %s", resp.StatusCode, body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read models response: %w", err)
	}

	var parsed rawModelListResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode models response: %w", err)
	}

	out := make([]backend.ModelInfo, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if m.ID == "" {
			continue
		}
		extras := map[string]string{}
		if m.Object != "" {
			extras["object"] = m.Object
		}
		if m.Created != 0 {
			extras["created"] = fmt.Sprintf("%d", m.Created)
		}
		if m.OwnedBy != "" {
			extras["owned_by"] = m.OwnedBy
		}
		if len(extras) == 0 {
			extras = nil
		}
		out = append(out, backend.ModelInfo{ID: m.ID, Vendor: "OpenAI", Extras: extras})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
