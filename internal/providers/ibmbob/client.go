package ibmbob

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// transientBodyMarkers reproduces the reference _RETRY_BODY_MARKERS list:
// non-5xx responses that nevertheless indicate a retryable upstream blip.
var transientBodyMarkers = []string{
	"Error getting user_id from apikey",
	"All connection attempts failed",
}

// maxRetryPeekBytes caps how much of a non-streaming error body we read to
// scan for transient-error markers. Larger error envelopes are not informative.
const maxRetryPeekBytes = 8 << 10

// forward issues an outbound request, retrying on retryable failures, and
// returns a backend.Response whose Body is owned by the caller.
//
// All current callers pass http.MethodPost; the method parameter is kept to
// keep the helper general for future endpoints (e.g. GET /v1/responses/{id}).
//
//nolint:unparam // method retained for future non-POST callers
func (p *Provider) forward(ctx context.Context, method, path string, body []byte, stream bool) (*backend.Response, error) {
	ctx, span := p.tracer.Start(ctx, "ibmbob.forward")
	defer span.End()
	span.SetAttributes(attribute.String("upstream.path", path), attribute.String("upstream.method", method), attribute.Bool("stream", stream))
	target, pathAndQuery, err := p.buildURL(path)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	var last *http.Response
	var lastErr error
	for attempt := 0; attempt < p.maxRetries; attempt++ {
		resp, err := p.doRequest(ctx, method, target, pathAndQuery, body)
		if err != nil {
			lastErr = err
			span.RecordError(err)
			span.AddEvent("request attempt failed", trace.WithAttributes(attribute.Int("attempt", attempt+1)))
			if attempt == p.maxRetries-1 {
				return nil, fmt.Errorf("ibmbob request failed after %d attempts: %w", p.maxRetries, err)
			}
			if sleepErr := sleepCtx(ctx, backoff(attempt, p.retryDelay)); sleepErr != nil {
				return nil, sleepErr
			}
			continue
		}
		if !shouldRetryResponse(resp, stream) {
			span.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))
			return &backend.Response{
				StatusCode: resp.StatusCode,
				Header:     resp.Header,
				Body:       resp.Body,
			}, nil
		}
		// retryable: drain and try again, unless this was the last attempt.
		if attempt == p.maxRetries-1 {
			span.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))
			return &backend.Response{
				StatusCode: resp.StatusCode,
				Header:     resp.Header,
				Body:       resp.Body,
			}, nil
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		span.AddEvent("retrying upstream response", trace.WithAttributes(attribute.Int("attempt", attempt+1), attribute.Int("status", resp.StatusCode)))
		last = resp
		if sleepErr := sleepCtx(ctx, backoff(attempt, p.retryDelay)); sleepErr != nil {
			return nil, sleepErr
		}
	}
	// Loop falls through only when maxRetries == 0, which is not a
	// legitimate configuration.
	if last != nil {
		return &backend.Response{StatusCode: last.StatusCode, Header: last.Header, Body: http.NoBody}, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("ibmbob: no attempts made (max_retries=%d)", p.maxRetries)
}

// doRequest performs a single HTTP attempt with all headers populated.
func (p *Provider) doRequest(ctx context.Context, method, target, pathAndQuery string, body []byte) (*http.Response, error) {
	ctx, span := p.tracer.Start(ctx, "ibmbob.do_request")
	defer span.End()
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("build request: %w", err)
	}
	p.setHeaders(req, pathAndQuery, body)
	resp, err := p.client.Do(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "upstream request")
		return nil, fmt.Errorf("ibmbob upstream: %w", err)
	}
	if resp == nil {
		span.SetStatus(codes.Error, "nil response")
		return nil, fmt.Errorf("ibmbob upstream: nil response")
	}
	return resp, nil
}

// setHeaders applies the full Bob header set, including HMAC signature when
// the URL/host require it.
func (p *Provider) setHeaders(req *http.Request, pathAndQuery string, body []byte) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", p.settings.UserAgent)
	req.Header.Set("Authorization", AuthorizationHeader(p.settings.APIKey, p.keyKind))

	id := p.currentIdentity()
	if id.InstanceID != "" {
		req.Header.Set("x-instance-id", id.InstanceID)
	}
	if id.TeamID != "" {
		req.Header.Set("x-team-id", id.TeamID)
	}

	if len(p.hmacSecret) > 0 && shouldSignURL(req.URL.String(), p.baseURL) {
		sig := SignRequest(req.Method, pathAndQuery, body, p.hmacSecret, p.now())
		req.Header.Set("x-request-timestamp", sig.Timestamp)
		req.Header.Set("x-request-signature", sig.Signature)
	}
}

// buildURL resolves an upstream URL from a path, returning both the full URL
// and the path-with-query string used as the second line of the canonical
// signing input.
func (p *Provider) buildURL(path string) (string, string, error) {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		parsed, err := url.Parse(path)
		if err != nil {
			return "", "", fmt.Errorf("parse path %q: %w", path, err)
		}
		return parsed.String(), pathWithQuery(parsed), nil
	}
	base, err := url.Parse(p.baseURL)
	if err != nil {
		return "", "", fmt.Errorf("parse base_url %q: %w", p.baseURL, err)
	}
	rel, err := url.Parse(path)
	if err != nil {
		return "", "", fmt.Errorf("parse path %q: %w", path, err)
	}
	full := base.ResolveReference(rel)
	return full.String(), pathWithQuery(full), nil
}

func pathWithQuery(u *url.URL) string {
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	if u.RawQuery != "" {
		return p + "?" + u.RawQuery
	}
	return p
}

// shouldRetryResponse mirrors the reference _should_retry_response: 5xx,
// 408, 429, and (for non-streaming responses) bodies containing one of the
// known transient markers.
func shouldRetryResponse(resp *http.Response, stream bool) bool {
	if resp.StatusCode < 400 {
		return false
	}
	if resp.StatusCode >= 500 || resp.StatusCode == 408 || resp.StatusCode == 429 {
		return true
	}
	if stream {
		return false
	}
	// Peek the body without consuming it - rewrap the bytes so callers
	// still see the full content.
	peeked, err := io.ReadAll(io.LimitReader(resp.Body, maxRetryPeekBytes))
	if err != nil {
		return false
	}
	rest, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	full := make([]byte, 0, len(peeked)+len(rest))
	full = append(full, peeked...)
	full = append(full, rest...)
	resp.Body = io.NopCloser(bytes.NewReader(full))
	hay := string(peeked)
	for _, marker := range transientBodyMarkers {
		if strings.Contains(hay, marker) {
			return true
		}
	}
	return false
}

func backoff(attempt int, base time.Duration) time.Duration {
	return base * (1 << attempt)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// fetchIdentity calls GET /admin/v1/profile and picks an (instance, team)
// pair the same way the reference implementation does.
func (p *Provider) fetchIdentity(ctx context.Context) (Identity, error) {
	ctx, span := p.tracer.Start(ctx, "ibmbob.fetch_identity")
	defer span.End()
	target, pathAndQuery, err := p.buildURL(profilePath)
	if err != nil {
		span.RecordError(err)
		return Identity{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return Identity{}, fmt.Errorf("build profile request: %w", err)
	}
	// setHeaders without any body bytes.
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", p.settings.UserAgent)
	req.Header.Set("Authorization", AuthorizationHeader(p.settings.APIKey, p.keyKind))
	if len(p.hmacSecret) > 0 && shouldSignURL(target, p.baseURL) {
		sig := SignRequest(http.MethodGet, pathAndQuery, nil, p.hmacSecret, p.now())
		req.Header.Set("x-request-timestamp", sig.Timestamp)
		req.Header.Set("x-request-signature", sig.Signature)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		span.RecordError(err)
		return Identity{}, fmt.Errorf("%w: %w", errProfileFetch, err)
	}
	if resp == nil {
		span.RecordError(errProfileFetch)
		return Identity{}, fmt.Errorf("%w: nil response", errProfileFetch)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Identity{}, fmt.Errorf("read profile body: %w", err)
	}
	if resp.StatusCode >= 400 {
		span.SetStatus(codes.Error, http.StatusText(resp.StatusCode))
		span.AddEvent("profile fetch failed", trace.WithAttributes(attribute.Int("status", resp.StatusCode)))
		return Identity{}, fmt.Errorf("%w: HTTP %d: %s", errProfileFetch, resp.StatusCode, truncate(string(body), 200))
	}
	return pickIdentity(body, p.settings.InstanceID, p.settings.TeamID)
}

// rawInstance / rawTeam reflect the documented fields of /admin/v1/profile.
type rawInstance struct {
	InstanceID string    `json:"instance_id"`
	Teams      []rawTeam `json:"teams"`
}

type rawTeam struct {
	ID string `json:"id"`
}

type rawProfile struct {
	Instances []rawInstance `json:"instances"`
}

func pickIdentity(body []byte, requestedInstance, requestedTeam string) (Identity, error) {
	var profile rawProfile
	if err := json.Unmarshal(body, &profile); err != nil {
		return Identity{}, fmt.Errorf("%w: decode: %w", errProfileFetch, err)
	}
	if len(profile.Instances) == 0 {
		// Mirror reference: keep whatever overrides the caller supplied.
		return Identity{InstanceID: requestedInstance, TeamID: requestedTeam}, nil
	}
	chosen := profile.Instances[0]
	if requestedInstance != "" {
		for _, inst := range profile.Instances {
			if inst.InstanceID == requestedInstance {
				chosen = inst
				break
			}
		}
	}
	id := Identity{InstanceID: chosen.InstanceID}
	if id.InstanceID == "" {
		id.InstanceID = requestedInstance
	}
	if len(chosen.Teams) > 0 {
		team := chosen.Teams[0]
		if requestedTeam != "" {
			for _, t := range chosen.Teams {
				if t.ID == requestedTeam {
					team = t
					break
				}
			}
		}
		id.TeamID = team.ID
	}
	if id.TeamID == "" {
		id.TeamID = requestedTeam
	}
	return id, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Unused outside the file but kept for symmetry/visibility.
var (
	_ = errors.Is
)
