package openai

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
)

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
	url := p.settings.BaseURL + "/models"
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

	return nil, backend.ErrUnsupported
}
