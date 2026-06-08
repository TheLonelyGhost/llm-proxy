package httplog

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const maxBodyBytes = 64 << 10 // 64 KiB, matches server.maxLoggedBodyBytes

// RoundTripper is an http.RoundTripper that logs upstream HTTP requests and
// responses at DEBUG level. When logBodies is true, request and response
// bodies are included (up to maxBodyBytes, after which they are truncated).
type RoundTripper struct {
	next      http.RoundTripper
	logger    *slog.Logger
	logBodies bool
}

// NewRoundTripper wraps next with upstream request/response logging at DEBUG.
// If next is nil, http.DefaultTransport is used.
func NewRoundTripper(next http.RoundTripper, logger *slog.Logger, logBodies bool) *RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return &RoundTripper{next: next, logger: logger, logBodies: logBodies}
}

// WrapClient wraps an http.Client's transport with a logging RoundTripper.
// If the client has no transport, http.DefaultTransport is wrapped.
func WrapClient(client *http.Client, logger *slog.Logger, logBodies bool) {
	next := client.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	client.Transport = NewRoundTripper(next, logger, logBodies)
}

// RoundTrip implements http.RoundTripper.
func (rt *RoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()

	var reqBody []byte
	var reqTruncated bool
	if rt.logBodies && req.Body != nil && req.Body != http.NoBody {
		reqBody, reqTruncated = readClamp(req.Body, maxBodyBytes)
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
	}

	resp, err := rt.next.RoundTrip(req)
	duration := time.Since(start)

	if err != nil {
		rt.logger.Debug("upstream request failed",
			"method", req.Method,
			"url", req.URL.String(),
			"duration_ms", duration.Milliseconds(),
			"err", err,
		)
		return resp, err
	}

	if resp == nil {
		return nil, nil
	}

	var respBody []byte
	var respTruncated bool
	if rt.logBodies && resp.Body != nil && resp.Body != http.NoBody {
		respBody, respTruncated = readClamp(resp.Body, maxBodyBytes)
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
	}

	attrs := []any{
		"method", req.Method,
		"url", req.URL.String(),
		"status", resp.StatusCode,
		"duration_ms", duration.Milliseconds(),
	}

	if rt.logBodies {
		if len(reqBody) > 0 {
			attrs = append(attrs,
				"request_body", string(reqBody),
				"request_body_truncated", reqTruncated,
			)
		}
		if len(respBody) > 0 {
			attrs = append(attrs,
				"response_body", string(respBody),
				"response_body_truncated", respTruncated,
			)
		}
	}

	rt.logger.Debug("upstream request", attrs...)

	return resp, nil
}

func readClamp(r io.ReadCloser, limit int) ([]byte, bool) {
	data, err := io.ReadAll(io.LimitReader(r, int64(limit+1)))
	if err != nil {
		return nil, false
	}
	_ = r.Close()
	truncated := len(data) > limit
	if truncated {
		data = data[:limit]
	}
	return data, truncated
}
