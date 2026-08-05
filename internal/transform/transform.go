// Package transform provides response transformation functions to normalize
// backend responses into OpenAI-compliant formats. It handles both non-streaming
// JSON responses and Server-Sent Events (SSE) streaming responses.
package transform

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
)

// TransformResult holds the transformed HTTP response data.
type TransformResult struct {
	StatusCode int
	Header     http.Header
	Body       io.ReadCloser
}

// TransformChatResponse transforms a backend chat completion response into
// OpenAI-compliant format. For streaming responses, it returns an SSE stream
// transformer. For non-streaming responses, it parses and normalizes the JSON
// structure, preserving the model prefix.
func TransformChatResponse(statusCode int, body io.ReadCloser, stream bool, modelPrefix string) (*TransformResult, error) {
	if stream {
		if statusCode >= 400 {
			defer func() { _ = body.Close() }()
			data, err := io.ReadAll(body)
			if err != nil {
				return nil, fmt.Errorf("read response body: %w", err)
			}
			return transformErrorResponse(statusCode, data)
		}
		return &TransformResult{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "Cache-Control": []string{"no-cache"}, "Connection": []string{"keep-alive"}},
			Body:       TransformStreamingChatResponse(body, modelPrefix),
		}, nil
	}

	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if statusCode >= 400 {
		return transformErrorResponse(statusCode, data)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		slog.Default().Debug("upstream chat response is not valid JSON", "err", err)
		return errorResponse(http.StatusBadGateway, "upstream_error", "upstream returned invalid JSON")
	}

	choices, ok := raw["choices"]
	if !ok {
		slog.Default().Debug("upstream chat response missing choices field")
		return errorResponse(http.StatusBadGateway, "upstream_error", "upstream response missing required field: choices")
	}

	transformed := ChatCompletionResponse{
		ID:      getStringOr(raw, "id", "chatcmpl-unknown"),
		Object:  "chat.completion",
		Created: getInt64Or(raw, "created", now()),
		Model:   modelPrefix,
		Choices: []ChatCompletionChoice{},
		Usage:   extractUsage(raw),
	}

	choicesArray, ok := choices.([]any)
	if !ok {
		slog.Default().Debug("upstream chat response has non-array choices field")
		return errorResponse(http.StatusBadGateway, "upstream_error", "upstream response has invalid choices field")
	}

	for _, c := range choicesArray {
		choice, ok := c.(map[string]any)
		if !ok {
			continue
		}

		finishReason := getStringPtr(choice, "finish_reason")
		message := extractMessage(choice, "message")

		// Normalize a broken upstream state: some backends (e.g. GitHub Copilot)
		// return finish_reason="tool_calls" without including a tool_calls array in
		// the message when the model decides voluntarily (no tool_choice forcing).
		// A valid OpenAI response with finish_reason="tool_calls" MUST include
		// tool_calls; without them the client cannot proceed. Downgrade to "stop".
		if finishReason != nil && *finishReason == "tool_calls" && (message == nil || len(message.ToolCalls) == 0) {
			stop := "stop"
			finishReason = &stop
		}

		transformed.Choices = append(transformed.Choices, ChatCompletionChoice{
			Index:        int(getInt64Or(choice, "index", 0)),
			Message:      message,
			FinishReason: finishReason,
		})
	}

	result, err := json.Marshal(transformed)
	if err != nil {
		return errorResponse(http.StatusInternalServerError, "internal_error", "failed to encode response")
	}

	return &TransformResult{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(result)),
	}, nil
}

// TransformCompletionResponse transforms a backend text completion response into
// OpenAI-compliant format. Handles both streaming and non-streaming responses,
// normalizing the choices array and logprobs field.
func TransformCompletionResponse(statusCode int, body io.ReadCloser, stream bool, modelPrefix string) (*TransformResult, error) {
	if stream {
		if statusCode >= 400 {
			defer func() { _ = body.Close() }()
			data, err := io.ReadAll(body)
			if err != nil {
				return nil, fmt.Errorf("read response body: %w", err)
			}
			return transformErrorResponse(statusCode, data)
		}
		return &TransformResult{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "Cache-Control": []string{"no-cache"}, "Connection": []string{"keep-alive"}},
			Body:       TransformStreamingCompletionResponse(body, modelPrefix),
		}, nil
	}

	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if statusCode >= 400 {
		return transformErrorResponse(statusCode, data)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		slog.Default().Debug("upstream completion response is not valid JSON", "err", err)
		return errorResponse(http.StatusBadGateway, "upstream_error", "upstream returned invalid JSON")
	}

	choices, ok := raw["choices"]
	if !ok {
		slog.Default().Debug("upstream completion response missing choices field")
		return errorResponse(http.StatusBadGateway, "upstream_error", "upstream response missing required field: choices")
	}

	transformed := CompletionResponse{
		ID:      getStringOr(raw, "id", "cmpl-unknown"),
		Object:  "text_completion",
		Created: getInt64Or(raw, "created", now()),
		Model:   modelPrefix,
		Choices: []CompletionChoice{},
		Usage:   extractUsage(raw),
	}

	choicesArray, ok := choices.([]any)
	if !ok {
		slog.Default().Debug("upstream completion response has non-array choices field")
		return errorResponse(http.StatusBadGateway, "upstream_error", "upstream response has invalid choices field")
	}

	for _, c := range choicesArray {
		choice, ok := c.(map[string]any)
		if !ok {
			continue
		}

		finishReason := getStringPtr(choice, "finish_reason")

		transformed.Choices = append(transformed.Choices, CompletionChoice{
			Index:        int(getInt64Or(choice, "index", 0)),
			Text:         getStringOr(choice, "text", ""),
			Logprobs:     nil,
			FinishReason: finishReason,
		})
	}

	result, err := json.Marshal(transformed)
	if err != nil {
		return errorResponse(http.StatusInternalServerError, "internal_error", "failed to encode response")
	}

	return &TransformResult{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(result)),
	}, nil
}

// TransformEmbeddingResponse transforms a backend embedding response into
// OpenAI-compliant format. Validates vector dimensions and handles batch inputs.
// Returns an error if embeddings contain non-numeric values or are empty.
func TransformEmbeddingResponse(statusCode int, body io.ReadCloser, modelPrefix string) (*TransformResult, error) {
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if statusCode >= 400 {
		return transformErrorResponse(statusCode, data)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		slog.Default().Debug("upstream embedding response is not valid JSON", "err", err)
		return errorResponse(http.StatusBadGateway, "upstream_error", "upstream returned invalid JSON")
	}

	dataField, ok := raw["data"]
	if !ok {
		slog.Default().Debug("upstream embedding response missing data field")
		return errorResponse(http.StatusBadGateway, "upstream_error", "upstream response missing required field: data")
	}

	transformed := EmbeddingResponse{
		Object: "list",
		Data:   []EmbeddingData{},
		Model:  modelPrefix,
		Usage:  extractUsage(raw),
	}

	if transformed.Usage == nil {
		transformed.Usage = &Usage{PromptTokens: 0, TotalTokens: 0}
	}

	dataArray, ok := dataField.([]any)
	if !ok {
		slog.Default().Debug("upstream embedding response has non-array data field")
		return errorResponse(http.StatusBadGateway, "upstream_error", "upstream response has invalid data field")
	}

	for _, d := range dataArray {
		item, ok := d.(map[string]any)
		if !ok {
			continue
		}

		embeddingField, ok := item["embedding"]
		if !ok {
			continue
		}

		embeddingArray, ok := embeddingField.([]any)
		if !ok || len(embeddingArray) == 0 {
			slog.Default().Debug("upstream embedding response has empty or non-array vector")
			return errorResponse(http.StatusBadGateway, "upstream_error", "upstream returned invalid embedding: empty or non-array vector")
		}

		embedding := make([]float64, 0, len(embeddingArray))
		for _, v := range embeddingArray {
			switch num := v.(type) {
			case float64:
				embedding = append(embedding, num)
			case int:
				embedding = append(embedding, float64(num))
			case int64:
				embedding = append(embedding, float64(num))
			default:
				slog.Default().Debug("upstream embedding response has non-numeric vector values")
				return errorResponse(http.StatusBadGateway, "upstream_error", "upstream returned invalid embedding: non-numeric values")
			}
		}

		transformed.Data = append(transformed.Data, EmbeddingData{
			Object:    "embedding",
			Index:     int(getInt64Or(item, "index", 0)),
			Embedding: embedding,
		})
	}

	result, err := json.Marshal(transformed)
	if err != nil {
		return errorResponse(http.StatusInternalServerError, "internal_error", "failed to encode response")
	}

	return &TransformResult{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(result)),
	}, nil
}

// TransformResponsesResponse transforms a backend responses endpoint response
// into OpenAI-compliant format. Handles reasoning output fields and reasoning_tokens
// in usage data. Supports both streaming and non-streaming responses.
//
// upstreamHeader is the HTTP header from the backend response. When the backend
// has already translated the stream into Responses-named SSE events (indicated by
// Content-Type: text/event-stream on a streaming request), the body is forwarded
// unchanged so that required fields (sequence_number, logprobs, etc.) emitted by
// the provider-level shim are not stripped by a second JSON round-trip.
func TransformResponsesResponse(statusCode int, body io.ReadCloser, stream bool, modelPrefix string, upstreamHeader http.Header) (*TransformResult, error) {
	if stream {
		if statusCode >= 400 {
			defer func() { _ = body.Close() }()
			data, err := io.ReadAll(body)
			if err != nil {
				return nil, fmt.Errorf("read response body: %w", err)
			}
			return transformErrorResponse(statusCode, data)
		}
		// If the backend already emitted a Responses-API SSE stream (e.g. via the
		// responsesshim), pass it through verbatim. Re-encoding would silently drop
		// required fields such as sequence_number, logprobs, and usage details.
		if upstreamHeader != nil && strings.Contains(upstreamHeader.Get("Content-Type"), "text/event-stream") {
			return &TransformResult{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "Cache-Control": []string{"no-cache"}, "Connection": []string{"keep-alive"}},
				Body:       body,
			}, nil
		}
		return &TransformResult{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "Cache-Control": []string{"no-cache"}, "Connection": []string{"keep-alive"}},
			Body:       TransformStreamingResponsesResponse(body, modelPrefix),
		}, nil
	}

	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if statusCode >= 400 {
		return transformErrorResponse(statusCode, data)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		slog.Default().Debug("upstream responses response is not valid JSON", "err", err)
		return errorResponse(http.StatusBadGateway, "upstream_error", "upstream returned invalid JSON")
	}
	if raw == nil {
		slog.Default().Debug("upstream responses response is empty JSON object")
		return errorResponse(http.StatusBadGateway, "upstream_error", "upstream returned empty JSON object")
	}

	// Native Responses API shape: top-level `output` array present.
	// Fully re-normalise: extract known fields, rewrite model, drop upstream-
	// specific fields (status, created_at) that don't belong in our output.
	if outputArr, hasOutput := raw["output"].([]any); hasOutput {
		id := getStringOr(raw, "id", "resp-unknown")

		// Upstream may use created_at (int) or created (int).
		var created int64
		if v, ok := raw["created_at"]; ok {
			switch n := v.(type) {
			case float64:
				created = int64(n)
			case int64:
				created = n
			}
		}
		if created == 0 {
			created = getInt64Or(raw, "created", now())
		}

		normalised := map[string]any{
			"id":         id,
			"object":     "response",
			"created_at": created,
			"model":      modelPrefix,
			"output":     outputArr,
		}
		if u, ok := raw["usage"]; ok {
			normalised["usage"] = u
		}
		out, err := json.Marshal(normalised)
		if err != nil {
			return errorResponse(http.StatusInternalServerError, "internal_error", "failed to re-encode response")
		}
		return &TransformResult{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(out)),
		}, nil
	}

	choices, ok := raw["choices"]
	if !ok {
		slog.Default().Debug("upstream responses response missing choices field")
		return errorResponse(http.StatusBadGateway, "upstream_error", "upstream response missing required field: choices")
	}

	transformed := ResponsesResponse{
		ID:      getStringOr(raw, "id", "resp-unknown"),
		Object:  "response",
		Created: getInt64Or(raw, "created", now()),
		Model:   modelPrefix,
		Choices: []ResponsesChoice{},
		Usage:   extractUsage(raw),
	}

	choicesArray, ok := choices.([]any)
	if !ok {
		slog.Default().Debug("upstream responses response has non-array choices field")
		return errorResponse(http.StatusBadGateway, "upstream_error", "upstream response has invalid choices field")
	}

	for _, c := range choicesArray {
		choice, ok := c.(map[string]any)
		if !ok {
			continue
		}

		finishReason := getStringPtr(choice, "finish_reason")
		message := extractMessage(choice, "message")

		transformed.Choices = append(transformed.Choices, ResponsesChoice{
			Index:        int(getInt64Or(choice, "index", 0)),
			Message:      message,
			FinishReason: finishReason,
		})
	}

	result, err := json.Marshal(transformed)
	if err != nil {
		return errorResponse(http.StatusInternalServerError, "internal_error", "failed to encode response")
	}

	return &TransformResult{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(result)),
	}, nil
}

// TransformErrorResponse transforms a backend error response into OpenAI-compliant
// error format. Maps HTTP status codes to OpenAI error types and preserves relevant
// headers like Retry-After. Handles both structured JSON errors and plain text responses.
func TransformErrorResponse(statusCode int, body io.ReadCloser, headers map[string][]string) (*backend.Response, error) {
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("read error response body: %w", err)
	}

	errType := mapStatusToErrorType(statusCode)
	message := "upstream returned error"

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err == nil {
		if errData, ok := raw["error"]; ok {
			if errMap, ok := errData.(map[string]any); ok {
				message = getStringOr(errMap, "message", message)
			}
		} else if msg, ok := raw["message"]; ok {
			if msgStr, ok := msg.(string); ok {
				message = msgStr
			}
		}
	} else {
		plainText := string(data)
		if len(plainText) > 0 && len(plainText) < 500 {
			message = plainText
		}
	}

	errResp := ErrorResponse{
		Error: ErrorDetail{
			Message: message,
			Type:    errType,
			Code:    nil,
		},
	}

	respData, _ := json.Marshal(errResp)
	respHeaders := http.Header{"Content-Type": []string{"application/json"}}

	if retryAfter, ok := headers["Retry-After"]; ok && len(retryAfter) > 0 {
		respHeaders.Set("Retry-After", retryAfter[0])
	}
	if rateLimitReset, ok := headers["X-RateLimit-Reset"]; ok && len(rateLimitReset) > 0 {
		respHeaders.Set("X-RateLimit-Reset", rateLimitReset[0])
	}

	return &backend.Response{
		StatusCode: statusCode,
		Header:     respHeaders,
		Body:       io.NopCloser(bytes.NewReader(respData)),
	}, nil
}

func getStringOr(m map[string]any, key string, defaultVal string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return defaultVal
}

func getInt64Or(m map[string]any, key string, defaultVal int64) int64 {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case int64:
			return n
		case int:
			return int64(n)
		case float64:
			return int64(n)
		}
	}
	return defaultVal
}

func getStringPtr(m map[string]any, key string) *string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return &s
		}
	}
	return nil
}

func extractMessage(choice map[string]any, key string) *Message {
	if v, ok := choice[key]; ok {
		if msg, ok := v.(map[string]any); ok {
			m := &Message{
				Role:    getStringOr(msg, "role", "assistant"),
				Content: getStringOr(msg, "content", ""),
			}
			if tcs, ok := msg["tool_calls"].([]any); ok {
				for _, tc := range tcs {
					tcMap, ok := tc.(map[string]any)
					if !ok {
						continue
					}
					fnMap, _ := tcMap["function"].(map[string]any)
					m.ToolCalls = append(m.ToolCalls, ToolCall{
						ID:   getStringOr(tcMap, "id", ""),
						Type: getStringOr(tcMap, "type", "function"),
						Function: ToolCallFunction{
							Name:      getStringOr(fnMap, "name", ""),
							Arguments: getStringOr(fnMap, "arguments", "{}"),
						},
					})
				}
			}
			return m
		}
	}
	return nil
}

func extractUsage(raw map[string]any) *Usage {
	if v, ok := raw["usage"]; ok {
		if usage, ok := v.(map[string]any); ok {
			return &Usage{
				PromptTokens:     int(getInt64Or(usage, "prompt_tokens", 0)),
				CompletionTokens: int(getInt64Or(usage, "completion_tokens", 0)),
				TotalTokens:      int(getInt64Or(usage, "total_tokens", 0)),
				ReasoningTokens:  int(getInt64Or(usage, "reasoning_tokens", 0)),
			}
		}
	}
	return nil
}

func transformErrorResponse(statusCode int, data []byte) (*TransformResult, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return errorResponse(statusCode, "upstream_error", string(data))
	}

	errType := mapStatusToErrorType(statusCode)
	message := "upstream returned error"
	if errData, ok := raw["error"]; ok {
		if errMap, ok := errData.(map[string]any); ok {
			message = getStringOr(errMap, "message", message)
		}
	}

	return errorResponse(statusCode, errType, message)
}

func errorResponse(statusCode int, errType string, message string) (*TransformResult, error) {
	errResp := ErrorResponse{
		Error: ErrorDetail{
			Message: message,
			Type:    errType,
			Code:    nil,
		},
	}
	data, _ := json.Marshal(errResp)
	return &TransformResult{
		StatusCode: statusCode,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(data)),
	}, nil
}

func mapStatusToErrorType(statusCode int) string {
	switch statusCode {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "invalid_request_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	default:
		return "upstream_error"
	}
}
