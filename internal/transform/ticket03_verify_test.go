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

	"github.com/thelonelyghost/llm-proxy/internal/backend"
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

// ticket03Resp wraps statusCode + body string into a *backend.Response.
func ticket03Resp(statusCode int, body string) *backend.Response {
	return &backend.Response{
		StatusCode: statusCode,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// --- Chat ---

func TestVerify_Ticket03_Chat_429_PropagatesStatus(t *testing.T) {
	result, err := Transform(
		ticket03Resp(http.StatusTooManyRequests, `{"error":{"message":"rate limit exceeded","type":"rate_limit_error"}}`),
		EndpointChat, true, "test/model",
	)
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
	result, err := Transform(
		ticket03Resp(http.StatusServiceUnavailable, `{"error":{"message":"service unavailable"}}`),
		EndpointChat, true, "test/model",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want %d", result.StatusCode, http.StatusServiceUnavailable)
	}
}

func TestVerify_Ticket03_Chat_ErrorBody_IsJSON_NotSSE(t *testing.T) {
	result, err := Transform(
		ticket03Resp(http.StatusForbidden, `{"error":{"message":"forbidden"}}`),
		EndpointChat, true, "test/model",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ct := result.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}
	readErrorEnvelope(t, result)
}

func TestVerify_Ticket03_Chat_200_StillReturnsSSEStream(t *testing.T) {
	result, err := Transform(
		ticket03Resp(http.StatusOK, "data: {\"id\":\"1\",\"choices\":[]}\n\n"),
		EndpointChat, true, "test/model",
	)
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

// --- Completion ---

func TestVerify_Ticket03_Completion_429_PropagatesStatus(t *testing.T) {
	result, err := Transform(
		ticket03Resp(http.StatusTooManyRequests, `{"error":{"message":"rate limit exceeded"}}`),
		EndpointCompletion, true, "test/model",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status: got %d, want %d", result.StatusCode, http.StatusTooManyRequests)
	}
	readErrorEnvelope(t, result)
}

func TestVerify_Ticket03_Completion_503_PropagatesStatus(t *testing.T) {
	result, err := Transform(
		ticket03Resp(http.StatusServiceUnavailable, `{"error":{"message":"service unavailable"}}`),
		EndpointCompletion, true, "test/model",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want %d", result.StatusCode, http.StatusServiceUnavailable)
	}
}

func TestVerify_Ticket03_Completion_ErrorBody_IsJSON_NotSSE(t *testing.T) {
	result, err := Transform(
		ticket03Resp(http.StatusForbidden, `{"error":{"message":"forbidden"}}`),
		EndpointCompletion, true, "test/model",
	)
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
	result, err := Transform(
		ticket03Resp(http.StatusOK, "data: {\"id\":\"1\",\"choices\":[]}\n\n"),
		EndpointCompletion, true, "test/model",
	)
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

// --- Responses ---

func TestVerify_Ticket03_Responses_429_PropagatesStatus(t *testing.T) {
	result, err := Transform(
		ticket03Resp(http.StatusTooManyRequests, `{"error":{"message":"rate limit exceeded"}}`),
		EndpointResponses, true, "test/model",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status: got %d, want %d", result.StatusCode, http.StatusTooManyRequests)
	}
	readErrorEnvelope(t, result)
}

func TestVerify_Ticket03_Responses_503_PropagatesStatus(t *testing.T) {
	result, err := Transform(
		ticket03Resp(http.StatusServiceUnavailable, `{"error":{"message":"service unavailable"}}`),
		EndpointResponses, true, "test/model",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want %d", result.StatusCode, http.StatusServiceUnavailable)
	}
}

func TestVerify_Ticket03_Responses_ErrorBody_IsJSON_NotSSE(t *testing.T) {
	result, err := Transform(
		ticket03Resp(http.StatusForbidden, `{"error":{"message":"forbidden"}}`),
		EndpointResponses, true, "test/model",
	)
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
	result, err := Transform(
		ticket03Resp(http.StatusOK, "event: response.created\ndata: {\"type\":\"response.created\"}\n\n"),
		EndpointResponses, true, "test/model",
	)
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
