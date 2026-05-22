package copilot

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// forward dispatches an upstream HTTP request to Copilot, refreshing the
// token once on a 401. The returned Response.Body is owned by the caller and
// must be closed.
func (p *Provider) forward(ctx context.Context, method, path string, body []byte, _ bool) (*backend.Response, error) {
	ctx, span := p.tracer.Start(ctx, "copilot.forward")
	defer span.End()
	span.SetAttributes(attribute.String("upstream.path", path), attribute.String("upstream.method", method))
	resp, err := p.doOnce(ctx, method, path, body)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		span.AddEvent("received 401; refreshing token")
		// Drain and close, then refresh and retry exactly once.
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		p.auth.Invalidate()
		retry, err := p.doOnce(ctx, method, path, body) //nolint:bodyclose // caller closes via backend.Response.Body
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "retry after 401")
			return nil, err
		}
		resp = retry
	}
	span.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))
	return &backend.Response{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       resp.Body,
	}, nil
}

func (p *Provider) doOnce(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	ctx, span := p.tracer.Start(ctx, "copilot.do_once")
	defer span.End()
	tok, err := p.auth.CopilotToken(ctx)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	url := p.settings.APIBase + path
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("build request: %w", err)
	}
	p.setHeaders(req, tok.Token)
	resp, err := p.http.Do(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "upstream request")
		return nil, fmt.Errorf("copilot request: %w", err)
	}
	if resp == nil {
		span.SetStatus(codes.Error, "nil response")
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
