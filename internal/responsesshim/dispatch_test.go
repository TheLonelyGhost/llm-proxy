package responsesshim_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
	"github.com/thelonelyghost/llm-proxy/internal/responsesshim"
)

// stubResponse builds a minimal *backend.Response for use in forwarder stubs.
func stubResponse(status int, body string, contentType string) *backend.Response {
	hdr := http.Header{}
	if contentType != "" {
		hdr.Set("Content-Type", contentType)
	}
	return &backend.Response{
		StatusCode: status,
		Header:     hdr,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// chatCompletionJSON is a minimal valid non-streaming /chat/completions response.
const chatCompletionJSON = `{
	"id":"chatcmpl-abc",
	"object":"chat.completion",
	"created":1700000000,
	"model":"gpt-4o",
	"choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],
	"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}
}`

// responsesRequestBody is a minimal valid /v1/responses request body (model
// already rewritten to the canonical id, as the router does before calling
// the backend).
const responsesRequestBody = `{"model":"gpt-4o","input":"Say hi"}`

func TestDispatch_NonStreaming_TranslatesResponse(t *testing.T) {
	req := backend.ResponsesRequest{
		Model:  "gpt-4o",
		Stream: false,
		Body:   []byte(responsesRequestBody),
	}

	var gotBody []byte
	fwd := func(_ context.Context, body []byte, stream bool) (*backend.Response, error) {
		gotBody = body
		if stream {
			t.Error("stream=true forwarded for non-streaming request")
		}
		return stubResponse(200, chatCompletionJSON, "application/json"), nil
	}

	resp, err := responsesshim.Dispatch(context.Background(), req, "copilot/gpt-4o", fwd)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	defer resp.Body.Close()

	// Forwarder received a translated /chat/completions body.
	if len(gotBody) == 0 {
		t.Fatal("forwarder received empty body")
	}
	// Response is re-encoded as Responses API shape.
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), `"object":"response"`) {
		t.Errorf("response body not Responses-API shaped:\n%s", raw)
	}
	if !strings.Contains(string(raw), `"model":"copilot/gpt-4o"`) {
		t.Errorf("model prefix not rewritten:\n%s", raw)
	}
}

func TestDispatch_NonStreaming_NonOK_PassThrough(t *testing.T) {
	req := backend.ResponsesRequest{
		Model:  "gpt-4o",
		Stream: false,
		Body:   []byte(responsesRequestBody),
	}

	errBody := `{"error":{"message":"unauthorized","type":"authentication_error"}}`
	fwd := func(_ context.Context, _ []byte, _ bool) (*backend.Response, error) {
		return stubResponse(401, errBody, "application/json"), nil
	}

	resp, err := responsesshim.Dispatch(context.Background(), req, "copilot/gpt-4o", fwd)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	defer resp.Body.Close()

	// Non-2xx must be passed through verbatim — no translation.
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	if string(raw) != errBody {
		t.Errorf("body not passed through:\ngot:  %s\nwant: %s", raw, errBody)
	}
}

func TestDispatch_ForwarderError_Propagated(t *testing.T) {
	sentinel := errors.New("network failure")
	req := backend.ResponsesRequest{
		Model: "gpt-4o",
		Body:  []byte(responsesRequestBody),
	}

	fwd := func(_ context.Context, _ []byte, _ bool) (*backend.Response, error) {
		return nil, sentinel
	}

	_, err := responsesshim.Dispatch(context.Background(), req, "copilot/gpt-4o", fwd)
	if !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel error, got %v", err)
	}
}

func TestDispatch_Streaming_WrapsBody(t *testing.T) {
	upstream := strings.Join([]string{
		`data: {"id":"chatcmpl-s","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		``,
		`data: {"id":"chatcmpl-s","choices":[{"index":0,"delta":{"content":"Hi"}}]}`,
		``,
		`data: {"id":"chatcmpl-s","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		``,
		`data: [DONE]`,
		``, ``,
	}, "\n")

	req := backend.ResponsesRequest{
		Model:  "gpt-4o",
		Stream: true,
		Body:   []byte(`{"model":"gpt-4o","input":"hi","stream":true}`),
	}

	fwd := func(_ context.Context, _ []byte, stream bool) (*backend.Response, error) {
		if !stream {
			t.Error("stream=false forwarded for streaming request")
		}
		return stubResponse(200, upstream, "text/event-stream"), nil
	}

	resp, err := responsesshim.Dispatch(context.Background(), req, "copilot/gpt-4o", fwd)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	// Body should emit Responses-API events.
	body, _ := io.ReadAll(resp.Body)
	got := string(body)
	for _, want := range []string{
		"event: response.created",
		"event: response.output_text.delta",
		"event: response.completed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("streaming body missing %q", want)
		}
	}
}

func TestDispatch_Streaming_NonOK_PassThrough(t *testing.T) {
	req := backend.ResponsesRequest{
		Model:  "gpt-4o",
		Stream: true,
		Body:   []byte(`{"model":"gpt-4o","input":"hi","stream":true}`),
	}

	errBody := `{"error":{"message":"rate limited"}}`
	fwd := func(_ context.Context, _ []byte, _ bool) (*backend.Response, error) {
		return stubResponse(429, errBody, "application/json"), nil
	}

	resp, err := responsesshim.Dispatch(context.Background(), req, "copilot/gpt-4o", fwd)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 429 {
		t.Errorf("status = %d, want 429", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	if string(raw) != errBody {
		t.Errorf("error body not passed through:\ngot:  %s\nwant: %s", raw, errBody)
	}
}

func TestDispatch_InvalidRequestBody_Error(t *testing.T) {
	req := backend.ResponsesRequest{
		Model: "gpt-4o",
		Body:  []byte(`not json`),
	}

	called := false
	fwd := func(_ context.Context, _ []byte, _ bool) (*backend.Response, error) {
		called = true
		return nil, nil
	}

	_, err := responsesshim.Dispatch(context.Background(), req, "copilot/gpt-4o", fwd)
	if err == nil {
		t.Fatal("expected error for invalid request body")
	}
	if called {
		t.Error("forwarder should not be called when request translation fails")
	}
}
