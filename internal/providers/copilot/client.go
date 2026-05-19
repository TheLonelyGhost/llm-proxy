package copilot

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
)

// forward dispatches an upstream HTTP request to Copilot, refreshing the
// token once on a 401. The returned Response.Body is owned by the caller and
// must be closed.
func (p *Provider) forward(ctx context.Context, method, path string, body []byte, _ bool) (*backend.Response, error) {
	resp, err := p.doOnce(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		// Drain and close, then refresh and retry exactly once.
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		p.auth.Invalidate()
		retry, err := p.doOnce(ctx, method, path, body) //nolint:bodyclose // caller closes via backend.Response.Body
		if err != nil {
			return nil, err
		}
		resp = retry
	}
	return &backend.Response{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       resp.Body,
	}, nil
}

func (p *Provider) doOnce(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	tok, err := p.auth.CopilotToken(ctx)
	if err != nil {
		return nil, err
	}
	url := p.settings.APIBase + path
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	p.setHeaders(req, tok.Token)
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("copilot request: %w", err)
	}
	if resp == nil {
		return nil, fmt.Errorf("nil response from HTTP client")
	}
	return resp, nil
}

func (p *Provider) setHeaders(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", p.settings.UserAgent)
	req.Header.Set("Editor-Version", p.settings.EditorVersion)
	req.Header.Set("Editor-Plugin-Version", p.settings.EditorPluginVersion)
	req.Header.Set("Copilot-Integration-Id", p.settings.IntegrationID)
	req.Header.Set("Openai-Intent", "conversation-panel")
}
