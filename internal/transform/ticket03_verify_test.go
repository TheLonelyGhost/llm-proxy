package transform

// Verification tests for ticket 03: upstream non-200 status codes must be
// propagated through the streaming path instead of being silently replaced
// with 200 OK.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// helper: drain the result body and unmarshal as an OpenAI error envelope.
func readErrorEnvelope(t *testing.T, r *TransformResult) ErrorResponse {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("reading result body: %v", err)
	}
	var env ErrorResponse
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("body is not a valid OpenAI error envelope: %v\nbody: %s", err, b)
	}
	return env
}

// --- TransformChatResponse ---

func TestVerify_Ticket03_Chat_429_PropagatesStatus(t *testing.T) {
	body := io.NopCloser(strings.NewReader(
		`{"error":{"message":"rate limit exceeded","type":"rate_limit_error"}}`,
	))
	result, err := TransformChatResponse(http.StatusTooManyRequests, body, true, "test/model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status: got %d, want %d", result.StatusCode, http.StatusTooManyRequests)
	}
	env := readErrorEnvelope(t, result)
	if env.Error.Message == "" {
		t.Error("error envelope message is empty")
	}
}

func TestVerify_Ticket03_Chat_503_PropagatesStatus(t *testing.T) {
	body := io.NopCloser(strings.NewReader(`{"error":{"message":"service unavailable"}}`))
	result, err := TransformChatResponse(http.StatusServiceUnavailable, body, true, "test/model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want %d", result.StatusCode, http.StatusServiceUnavailable)
	}
}

func TestVerify_Ticket03_Chat_ErrorBody_IsJSON_NotSSE(t *testing.T) {
	body := io.NopCloser(strings.NewReader(`{"error":{"message":"forbidden"}}`))
	result, err := TransformChatResponse(http.StatusForbidden, body, true, "test/model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ct := result.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}
	// Body must parse as an error envelope, not as an SSE stream.
	readErrorEnvelope(t, result)
}

func TestVerify_Ticket03_Chat_200_StillReturnsSSEStream(t *testing.T) {
	body := io.NopCloser(strings.NewReader("data: {\"id\":\"1\",\"choices\":[]}\n\n"))
	result, err := TransformChatResponse(http.StatusOK, body, true, "test/model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", result.StatusCode)
	}
	ct := result.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type: got %q, want text/event-stream", ct)
	}
}

// --- TransformCompletionResponse ---

func TestVerify_Ticket03_Completion_429_PropagatesStatus(t *testing.T) {
	body := io.NopCloser(strings.NewReader(`{"error":{"message":"rate limit exceeded"}}`))
	result, err := TransformCompletionResponse(http.StatusTooManyRequests, body, true, "test/model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status: got %d, want %d", result.StatusCode, http.StatusTooManyRequests)
	}
	readErrorEnvelope(t, result)
}

func TestVerify_Ticket03_Completion_503_PropagatesStatus(t *testing.T) {
	body := io.NopCloser(strings.NewReader(`{"error":{"message":"service unavailable"}}`))
	result, err := TransformCompletionResponse(http.StatusServiceUnavailable, body, true, "test/model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want %d", result.StatusCode, http.StatusServiceUnavailable)
	}
}

func TestVerify_Ticket03_Completion_ErrorBody_IsJSON_NotSSE(t *testing.T) {
	body := io.NopCloser(strings.NewReader(`{"error":{"message":"forbidden"}}`))
	result, err := TransformCompletionResponse(http.StatusForbidden, body, true, "test/model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ct := result.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}
	readErrorEnvelope(t, result)
}

func TestVerify_Ticket03_Completion_200_StillReturnsSSEStream(t *testing.T) {
	body := io.NopCloser(strings.NewReader("data: {\"id\":\"1\",\"choices\":[]}\n\n"))
	result, err := TransformCompletionResponse(http.StatusOK, body, true, "test/model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", result.StatusCode)
	}
	ct := result.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type: got %q, want text/event-stream", ct)
	}
}

// --- TransformResponsesResponse ---

func TestVerify_Ticket03_Responses_429_PropagatesStatus(t *testing.T) {
	body := io.NopCloser(strings.NewReader(`{"error":{"message":"rate limit exceeded"}}`))
	result, err := TransformResponsesResponse(http.StatusTooManyRequests, body, true, "test/model", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status: got %d, want %d", result.StatusCode, http.StatusTooManyRequests)
	}
	readErrorEnvelope(t, result)
}

func TestVerify_Ticket03_Responses_503_PropagatesStatus(t *testing.T) {
	body := io.NopCloser(strings.NewReader(`{"error":{"message":"service unavailable"}}`))
	result, err := TransformResponsesResponse(http.StatusServiceUnavailable, body, true, "test/model", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want %d", result.StatusCode, http.StatusServiceUnavailable)
	}
}

func TestVerify_Ticket03_Responses_ErrorBody_IsJSON_NotSSE(t *testing.T) {
	body := io.NopCloser(strings.NewReader(`{"error":{"message":"forbidden"}}`))
	result, err := TransformResponsesResponse(http.StatusForbidden, body, true, "test/model", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ct := result.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}
	readErrorEnvelope(t, result)
}

func TestVerify_Ticket03_Responses_200_StillReturnsSSEStream(t *testing.T) {
	body := io.NopCloser(strings.NewReader("event: response.created\ndata: {\"type\":\"response.created\"}\n\n"))
	result, err := TransformResponsesResponse(http.StatusOK, body, true, "test/model", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", result.StatusCode)
	}
	ct := result.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type: got %q, want text/event-stream", ct)
	}
}
