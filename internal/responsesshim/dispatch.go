package responsesshim

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
)

// Forwarder is the seam between Dispatch and a provider's transport layer.
// It receives the translated /chat/completions body and the stream flag, and
// returns the raw upstream response. The returned Response.Body is owned by
// Dispatch and will be closed by it or handed to the caller as-is.
//
// Implementations are typically a closure that captures the provider's HTTP
// client, base URL, and any auth headers — nothing in Dispatch touches those
// concerns.
type Forwarder func(ctx context.Context, body []byte, stream bool) (*backend.Response, error)

// Dispatch is the full translate→forward→translate pipeline shared by every
// provider whose upstream only speaks Chat Completions.
//
// It:
//  1. Translates req.Body from the Responses API shape to a /chat/completions body.
//  2. Calls fwd to send the translated body through the provider's transport.
//  3. Passes non-2xx responses through unchanged (the server handles error envelopes).
//  4. For streaming 2xx: wraps the upstream SSE body with TranslateChatStreamToResponses
//     and sets the appropriate response headers.
//  5. For non-streaming 2xx: reads the body, calls TranslateChatCompletion, and
//     returns a complete response with the Responses API JSON body.
//
// modelPrefix is the fully-qualified model identifier the client sent
// (e.g. "copilot/gpt-4o"). Providers compute this as name+"/"+req.Model.
func Dispatch(ctx context.Context, req backend.ResponsesRequest, modelPrefix string, fwd Forwarder) (*backend.Response, error) {
	chatBody, _, _, err := TranslateRequest(req.Body)
	if err != nil {
		return nil, fmt.Errorf("translate /v1/responses request: %w", err)
	}

	resp, err := fwd(ctx, chatBody, req.Stream)
	if err != nil {
		return nil, err
	}

	// Non-2xx: pass through so the server's error-envelope handling fires.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, nil
	}

	if req.Stream {
		resp.Body = TranslateChatStreamToResponses(resp.Body, modelPrefix)
		hdr := cloneHeader(resp.Header)
		hdr.Set("Content-Type", "text/event-stream")
		hdr.Set("Cache-Control", "no-cache")
		resp.Header = hdr
		return resp, nil
	}

	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read upstream chat response: %w", err)
	}
	translated, err := TranslateChatCompletion(raw, modelPrefix)
	if err != nil {
		return nil, err
	}
	hdr := cloneHeader(resp.Header)
	hdr.Set("Content-Type", "application/json")
	return &backend.Response{
		StatusCode: resp.StatusCode,
		Header:     hdr,
		Body:       io.NopCloser(bytes.NewReader(translated)),
	}, nil
}

// cloneHeader returns a shallow copy of h so callers can mutate it without
// affecting upstream-owned state.
func cloneHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, vv := range h {
		out[k] = append([]string(nil), vv...)
	}
	return out
}
