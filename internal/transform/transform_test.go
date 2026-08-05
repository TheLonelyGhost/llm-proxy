package transform

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
)

// resp builds a minimal *backend.Response for use in table-driven tests.
func resp(statusCode int, body string, header http.Header) *backend.Response {
	if header == nil {
		header = http.Header{}
	}
	return &backend.Response{
		StatusCode: statusCode,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestTransformChatResponse(t *testing.T) {
	tests := []struct {
		name         string
		statusCode   int
		responseBody string
		modelPrefix  string
		stream       bool
		wantStatus   int
		wantErr      bool
		validate     func(t *testing.T, result *TransformResult)
	}{
		{
			name:       "valid chat response",
			statusCode: 200,
			responseBody: `{
				"id": "chatcmpl-123",
				"object": "chat.completion",
				"created": 1677652288,
				"model": "gpt-4",
				"choices": [{
					"index": 0,
					"message": {
						"role": "assistant",
						"content": "Hello! How can I help you?"
					},
					"finish_reason": "stop"
				}],
				"usage": {
					"prompt_tokens": 10,
					"completion_tokens": 20,
					"total_tokens": 30
				}
			}`,
			modelPrefix: "openai/gpt-4",
			stream:      false,
			wantStatus:  200,
			wantErr:     false,
			validate: func(t *testing.T, result *TransformResult) {
				body, _ := io.ReadAll(result.Body)
				var resp ChatCompletionResponse
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("failed to unmarshal response: %v", err)
				}
				if resp.Object != "chat.completion" {
					t.Errorf("expected object=chat.completion, got %s", resp.Object)
				}
				if resp.Model != "openai/gpt-4" {
					t.Errorf("expected model=openai/gpt-4, got %s", resp.Model)
				}
				if len(resp.Choices) != 1 {
					t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
				}
				if resp.Choices[0].Message.Content != "Hello! How can I help you?" {
					t.Errorf("unexpected message content: %s", resp.Choices[0].Message.Content)
				}
				if resp.Usage.PromptTokens != 10 {
					t.Errorf("expected prompt_tokens=10, got %d", resp.Usage.PromptTokens)
				}
			},
		},
		{
			name:       "missing choices field",
			statusCode: 200,
			responseBody: `{
				"id": "chatcmpl-123",
				"object": "chat.completion",
				"created": 1677652288,
				"model": "gpt-4"
			}`,
			modelPrefix: "openai/gpt-4",
			stream:      false,
			wantStatus:  502,
			wantErr:     false,
			validate: func(t *testing.T, result *TransformResult) {
				body, _ := io.ReadAll(result.Body)
				var resp ErrorResponse
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("failed to unmarshal error response: %v", err)
				}
				if !strings.Contains(resp.Error.Message, "choices") {
					t.Errorf("expected error about choices, got: %s", resp.Error.Message)
				}
			},
		},
		{
			name:         "invalid JSON",
			statusCode:   200,
			responseBody: `{invalid json`,
			modelPrefix:  "openai/gpt-4",
			stream:       false,
			wantStatus:   502,
			wantErr:      false,
			validate: func(t *testing.T, result *TransformResult) {
				body, _ := io.ReadAll(result.Body)
				var resp ErrorResponse
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("failed to unmarshal error response: %v", err)
				}
				if !strings.Contains(resp.Error.Message, "JSON") {
					t.Errorf("expected error about JSON, got: %s", resp.Error.Message)
				}
			},
		},
		{
			name:       "empty choices array",
			statusCode: 200,
			responseBody: `{
				"id": "chatcmpl-123",
				"choices": []
			}`,
			modelPrefix: "openai/gpt-4",
			stream:      false,
			wantStatus:  200,
			wantErr:     false,
			validate: func(t *testing.T, result *TransformResult) {
				body, _ := io.ReadAll(result.Body)
				var resp ChatCompletionResponse
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("failed to unmarshal response: %v", err)
				}
				if len(resp.Choices) != 0 {
					t.Errorf("expected 0 choices, got %d", len(resp.Choices))
				}
			},
		},
		{
			name:       "choices with invalid structure",
			statusCode: 200,
			responseBody: `{
				"id": "chatcmpl-123",
				"choices": [
					{"index": 0, "message": "not an object"}
				]
			}`,
			modelPrefix: "openai/gpt-4",
			stream:      false,
			wantStatus:  200,
			wantErr:     false,
			validate: func(t *testing.T, result *TransformResult) {
				body, _ := io.ReadAll(result.Body)
				var resp ChatCompletionResponse
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("failed to unmarshal response: %v", err)
				}
				if len(resp.Choices) != 1 {
					t.Errorf("expected 1 choice, got %d", len(resp.Choices))
				}
				if resp.Choices[0].Message != nil {
					t.Errorf("expected nil message for invalid structure")
				}
			},
		},
		{
			name:       "finish_reason tool_calls with tool_calls present — preserved",
			statusCode: 200,
			responseBody: `{
				"id": "chatcmpl-tc1",
				"object": "chat.completion",
				"created": 1677652288,
				"model": "gpt-4",
				"choices": [{
					"index": 0,
					"message": {
						"role": "assistant",
						"content": "",
						"tool_calls": [{
							"id": "call_abc123",
							"type": "function",
							"function": {
								"name": "add",
								"arguments": "{\"a\":3,\"b\":4}"
							}
						}]
					},
					"finish_reason": "tool_calls"
				}],
				"usage": {"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30}
			}`,
			modelPrefix: "copilot/claude-sonnet",
			stream:      false,
			wantStatus:  200,
			wantErr:     false,
			validate: func(t *testing.T, result *TransformResult) {
				body, _ := io.ReadAll(result.Body)
				var resp ChatCompletionResponse
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("failed to unmarshal: %v", err)
				}
				if *resp.Choices[0].FinishReason != "tool_calls" {
					t.Errorf("expected finish_reason=tool_calls, got %s", *resp.Choices[0].FinishReason)
				}
				if len(resp.Choices[0].Message.ToolCalls) != 1 {
					t.Fatalf("expected 1 tool call, got %d", len(resp.Choices[0].Message.ToolCalls))
				}
				if resp.Choices[0].Message.ToolCalls[0].Function.Name != "add" {
					t.Errorf("expected tool name=add, got %s", resp.Choices[0].Message.ToolCalls[0].Function.Name)
				}
			},
		},
		{
			name:       "finish_reason tool_calls with missing tool_calls — normalized to stop",
			statusCode: 200,
			responseBody: `{
				"id": "chatcmpl-tc2",
				"object": "chat.completion",
				"created": 1677652288,
				"model": "gpt-4",
				"choices": [{
					"index": 0,
					"message": {
						"role": "assistant",
						"content": "Sure! Let me add those numbers for you."
					},
					"finish_reason": "tool_calls"
				}],
				"usage": {"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30}
			}`,
			modelPrefix: "copilot/claude-sonnet",
			stream:      false,
			wantStatus:  200,
			wantErr:     false,
			validate: func(t *testing.T, result *TransformResult) {
				body, _ := io.ReadAll(result.Body)
				var resp ChatCompletionResponse
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("failed to unmarshal: %v", err)
				}
				if *resp.Choices[0].FinishReason != "stop" {
					t.Errorf("expected finish_reason=stop (normalized), got %s", *resp.Choices[0].FinishReason)
				}
				if resp.Choices[0].Message.Content != "Sure! Let me add those numbers for you." {
					t.Errorf("content was unexpectedly modified: %s", resp.Choices[0].Message.Content)
				}
				if len(resp.Choices[0].Message.ToolCalls) != 0 {
					t.Errorf("expected no tool calls, got %d", len(resp.Choices[0].Message.ToolCalls))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Transform(resp(tt.statusCode, tt.responseBody, nil), EndpointChat, tt.stream, tt.modelPrefix)

			if (err != nil) != tt.wantErr {
				t.Errorf("Transform(EndpointChat) error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if result.StatusCode != tt.wantStatus {
				t.Errorf("expected status %d, got %d", tt.wantStatus, result.StatusCode)
			}

			if tt.validate != nil {
				tt.validate(t, result)
			}
		})
	}
}

func TestTransformCompletionResponse(t *testing.T) {
	tests := []struct {
		name         string
		statusCode   int
		responseBody string
		modelPrefix  string
		stream       bool
		wantStatus   int
		wantErr      bool
		validate     func(t *testing.T, result *TransformResult)
	}{
		{
			name:       "valid completion response",
			statusCode: 200,
			responseBody: `{
				"id": "cmpl-123",
				"object": "text_completion",
				"created": 1677652288,
				"model": "gpt-3.5-turbo-instruct",
				"choices": [{
					"index": 0,
					"text": "This is a test completion",
					"finish_reason": "stop"
				}],
				"usage": {
					"prompt_tokens": 5,
					"completion_tokens": 15,
					"total_tokens": 20
				}
			}`,
			modelPrefix: "openai/gpt-3.5-turbo-instruct",
			stream:      false,
			wantStatus:  200,
			wantErr:     false,
			validate: func(t *testing.T, result *TransformResult) {
				body, _ := io.ReadAll(result.Body)
				var resp CompletionResponse
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("failed to unmarshal response: %v", err)
				}
				if resp.Object != "text_completion" {
					t.Errorf("expected object=text_completion, got %s", resp.Object)
				}
				if resp.Model != "openai/gpt-3.5-turbo-instruct" {
					t.Errorf("expected model prefix, got %s", resp.Model)
				}
				if len(resp.Choices) != 1 {
					t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
				}
				if resp.Choices[0].Text != "This is a test completion" {
					t.Errorf("unexpected text: %s", resp.Choices[0].Text)
				}
			},
		},
		{
			name:       "invalid choices field type",
			statusCode: 200,
			responseBody: `{
				"id": "cmpl-123",
				"choices": "not an array"
			}`,
			modelPrefix: "openai/test",
			stream:      false,
			wantStatus:  502,
			wantErr:     false,
		},
		{
			name:         "malformed JSON",
			statusCode:   200,
			responseBody: `not valid json at all`,
			modelPrefix:  "openai/test",
			stream:       false,
			wantStatus:   502,
			wantErr:      false,
		},
		{
			name:       "missing choices field",
			statusCode: 200,
			responseBody: `{
				"id": "cmpl-123",
				"model": "test"
			}`,
			modelPrefix: "openai/test",
			stream:      false,
			wantStatus:  502,
			wantErr:     false,
		},
		{
			name:       "empty choices array",
			statusCode: 200,
			responseBody: `{
				"id": "cmpl-123",
				"choices": []
			}`,
			modelPrefix: "openai/test",
			stream:      false,
			wantStatus:  200,
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Transform(resp(tt.statusCode, tt.responseBody, nil), EndpointCompletion, tt.stream, tt.modelPrefix)

			if (err != nil) != tt.wantErr {
				t.Errorf("Transform(EndpointCompletion) error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if result.StatusCode != tt.wantStatus {
				t.Errorf("expected status %d, got %d", tt.wantStatus, result.StatusCode)
			}

			if tt.validate != nil {
				tt.validate(t, result)
			}
		})
	}
}

func TestTransformEmbeddingResponse(t *testing.T) {
	tests := []struct {
		name         string
		statusCode   int
		responseBody string
		modelPrefix  string
		wantStatus   int
		wantErr      bool
		validate     func(t *testing.T, result *TransformResult)
	}{
		{
			name:       "valid embedding response",
			statusCode: 200,
			responseBody: `{
				"object": "list",
				"data": [{
					"object": "embedding",
					"index": 0,
					"embedding": [0.1, 0.2, 0.3, 0.4]
				}],
				"model": "text-embedding-ada-002",
				"usage": {
					"prompt_tokens": 8,
					"total_tokens": 8
				}
			}`,
			modelPrefix: "openai/text-embedding-ada-002",
			wantStatus:  200,
			wantErr:     false,
			validate: func(t *testing.T, result *TransformResult) {
				body, _ := io.ReadAll(result.Body)
				var resp EmbeddingResponse
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("failed to unmarshal response: %v", err)
				}
				if resp.Object != "list" {
					t.Errorf("expected object=list, got %s", resp.Object)
				}
				if resp.Model != "openai/text-embedding-ada-002" {
					t.Errorf("expected model prefix, got %s", resp.Model)
				}
				if len(resp.Data) != 1 {
					t.Fatalf("expected 1 embedding, got %d", len(resp.Data))
				}
				if len(resp.Data[0].Embedding) != 4 {
					t.Errorf("expected 4 dimensions, got %d", len(resp.Data[0].Embedding))
				}
			},
		},
		{
			name:       "batch embeddings",
			statusCode: 200,
			responseBody: `{
				"object": "list",
				"data": [
					{
						"object": "embedding",
						"index": 0,
						"embedding": [0.1, 0.2]
					},
					{
						"object": "embedding",
						"index": 1,
						"embedding": [0.3, 0.4]
					}
				],
				"model": "text-embedding-ada-002"
			}`,
			modelPrefix: "openai/text-embedding-ada-002",
			wantStatus:  200,
			wantErr:     false,
			validate: func(t *testing.T, result *TransformResult) {
				body, _ := io.ReadAll(result.Body)
				var resp EmbeddingResponse
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("failed to unmarshal response: %v", err)
				}
				if len(resp.Data) != 2 {
					t.Fatalf("expected 2 embeddings, got %d", len(resp.Data))
				}
			},
		},
		{
			name:       "empty embedding vector",
			statusCode: 200,
			responseBody: `{
				"object": "list",
				"data": [{
					"object": "embedding",
					"index": 0,
					"embedding": []
				}]
			}`,
			modelPrefix: "openai/test",
			wantStatus:  502,
			wantErr:     false,
			validate: func(t *testing.T, result *TransformResult) {
				body, _ := io.ReadAll(result.Body)
				var resp ErrorResponse
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("failed to unmarshal error response: %v", err)
				}
				if !strings.Contains(resp.Error.Message, "embedding") {
					t.Errorf("expected error about embedding, got: %s", resp.Error.Message)
				}
			},
		},
		{
			name:       "missing data field",
			statusCode: 200,
			responseBody: `{
				"object": "list",
				"model": "test"
			}`,
			modelPrefix: "openai/test",
			wantStatus:  502,
			wantErr:     false,
		},
		{
			name:         "invalid JSON",
			statusCode:   200,
			responseBody: `{broken json`,
			modelPrefix:  "openai/test",
			wantStatus:   502,
			wantErr:      false,
		},
		{
			name:       "data field not an array",
			statusCode: 200,
			responseBody: `{
				"object": "list",
				"data": "not an array"
			}`,
			modelPrefix: "openai/test",
			wantStatus:  502,
			wantErr:     false,
		},
		{
			name:       "non-numeric embedding values",
			statusCode: 200,
			responseBody: `{
				"object": "list",
				"data": [{
					"object": "embedding",
					"index": 0,
					"embedding": [0.1, "invalid", 0.3]
				}]
			}`,
			modelPrefix: "openai/test",
			wantStatus:  502,
			wantErr:     false,
		},
		{
			name:       "embedding not an array",
			statusCode: 200,
			responseBody: `{
				"object": "list",
				"data": [{
					"object": "embedding",
					"index": 0,
					"embedding": "not an array"
				}]
			}`,
			modelPrefix: "openai/test",
			wantStatus:  502,
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Transform(resp(tt.statusCode, tt.responseBody, nil), EndpointEmbedding, false, tt.modelPrefix)

			if (err != nil) != tt.wantErr {
				t.Errorf("Transform(EndpointEmbedding) error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if result.StatusCode != tt.wantStatus {
				t.Errorf("expected status %d, got %d", tt.wantStatus, result.StatusCode)
			}

			if tt.validate != nil {
				tt.validate(t, result)
			}
		})
	}
}

func TestTransformResponsesResponse(t *testing.T) {
	tests := []struct {
		name         string
		statusCode   int
		responseBody string
		modelPrefix  string
		stream       bool
		wantStatus   int
		wantErr      bool
		validate     func(t *testing.T, result *TransformResult)
	}{
		{
			name:       "valid responses response",
			statusCode: 200,
			responseBody: `{
				"id": "resp-123",
				"object": "response",
				"created": 1677652288,
				"model": "o1-preview",
				"choices": [{
					"index": 0,
					"message": {
						"role": "assistant",
						"content": "After careful reasoning, the answer is 42."
					},
					"finish_reason": "stop"
				}],
				"usage": {
					"prompt_tokens": 50,
					"completion_tokens": 100,
					"reasoning_tokens": 80,
					"total_tokens": 150
				}
			}`,
			modelPrefix: "openai/o1-preview",
			stream:      false,
			wantStatus:  200,
			wantErr:     false,
			validate: func(t *testing.T, result *TransformResult) {
				body, _ := io.ReadAll(result.Body)
				var resp ResponsesResponse
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("failed to unmarshal response: %v", err)
				}
				if resp.Object != "response" {
					t.Errorf("expected object=response, got %s", resp.Object)
				}
				if resp.Model != "openai/o1-preview" {
					t.Errorf("expected model prefix, got %s", resp.Model)
				}
				if len(resp.Choices) != 1 {
					t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
				}
				if resp.Usage == nil {
					t.Fatal("expected usage to be present")
				}
				if resp.Usage.ReasoningTokens != 80 {
					t.Errorf("expected reasoning_tokens=80, got %d", resp.Usage.ReasoningTokens)
				}
			},
		},
		{
			name:       "responses without reasoning tokens",
			statusCode: 200,
			responseBody: `{
				"id": "resp-456",
				"object": "response",
				"created": 1677652288,
				"model": "o1-mini",
				"choices": [{
					"index": 0,
					"message": {
						"role": "assistant",
						"content": "Quick answer"
					},
					"finish_reason": "stop"
				}],
				"usage": {
					"prompt_tokens": 10,
					"completion_tokens": 5,
					"total_tokens": 15
				}
			}`,
			modelPrefix: "openai/o1-mini",
			stream:      false,
			wantStatus:  200,
			wantErr:     false,
			validate: func(t *testing.T, result *TransformResult) {
				body, _ := io.ReadAll(result.Body)
				var resp ResponsesResponse
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("failed to unmarshal response: %v", err)
				}
				if resp.Usage.ReasoningTokens != 0 {
					t.Errorf("expected reasoning_tokens=0, got %d", resp.Usage.ReasoningTokens)
				}
			},
		},
		{
			name:         "invalid JSON",
			statusCode:   200,
			responseBody: `{malformed`,
			modelPrefix:  "openai/o1",
			stream:       false,
			wantStatus:   502,
			wantErr:      false,
		},
		{
			name:       "missing choices field",
			statusCode: 200,
			responseBody: `{
				"id": "resp-789",
				"model": "o1"
			}`,
			modelPrefix: "openai/o1",
			stream:      false,
			wantStatus:  502,
			wantErr:     false,
		},
		{
			name:       "choices not an array",
			statusCode: 200,
			responseBody: `{
				"id": "resp-789",
				"choices": "not an array"
			}`,
			modelPrefix: "openai/o1",
			stream:      false,
			wantStatus:  502,
			wantErr:     false,
		},
		{
			name:       "empty choices array",
			statusCode: 200,
			responseBody: `{
				"id": "resp-789",
				"choices": []
			}`,
			modelPrefix: "openai/o1",
			stream:      false,
			wantStatus:  200,
			wantErr:     false,
		},
		{
			// Native Responses API shape (top-level `output` array) must be
			// fully re-normalised, not passed through with only a model rewrite.
			// ADR-0005: the pass-through was unintentional.
			name:       "native responses shape is fully re-normalised with qualified model id",
			statusCode: 200,
			responseBody: `{
				"id": "resp-native-1",
				"object": "response",
				"created_at": 1677652288,
				"status": "completed",
				"model": "o1",
				"output": [{
					"type": "message",
					"id": "msg-1",
					"status": "completed",
					"role": "assistant",
					"content": [{"type": "output_text", "text": "Hello", "annotations": []}]
				}],
				"usage": {
					"input_tokens": 10,
					"output_tokens": 5,
					"total_tokens": 15
				}
			}`,
			modelPrefix: "openai/o1",
			stream:      false,
			wantStatus:  200,
			wantErr:     false,
			validate: func(t *testing.T, result *TransformResult) {
				body, _ := io.ReadAll(result.Body)
				var raw map[string]any
				if err := json.Unmarshal(body, &raw); err != nil {
					t.Fatalf("failed to unmarshal response: %v", err)
				}
				// model must be the Qualified Model ID, not the upstream canonical id
				if raw["model"] != "openai/o1" {
					t.Errorf("expected model=openai/o1, got %v", raw["model"])
				}
				// id must be preserved from upstream
				if raw["id"] != "resp-native-1" {
					t.Errorf("expected id=resp-native-1, got %v", raw["id"])
				}
				// object must be normalised to "response"
				if raw["object"] != "response" {
					t.Errorf("expected object=response, got %v", raw["object"])
				}
				// upstream uses created_at; normalised output must preserve created_at (OpenAI spec)
				if _, hasCreatedAt := raw["created_at"]; !hasCreatedAt {
					t.Error("normalised response must have a \"created_at\" field")
				}
				if _, hasCreated := raw["created"]; hasCreated {
					t.Error("normalised response must use \"created_at\", not \"created\"")
				}
				// output array must be present and intact
				output, ok := raw["output"].([]any)
				if !ok || len(output) != 1 {
					t.Fatalf("expected output array with 1 item, got %v", raw["output"])
				}
				item, ok := output[0].(map[string]any)
				if !ok {
					t.Fatal("expected output[0] to be an object")
				}
				if item["type"] != "message" {
					t.Errorf("expected output[0].type=message, got %v", item["type"])
				}
				// must NOT contain leftover upstream fields like `choices` or `status`
				if _, hasChoices := raw["choices"]; hasChoices {
					t.Error("normalised response must not contain a \"choices\" field")
				}
				if _, hasStatus := raw["status"]; hasStatus {
					t.Error("normalised response must not contain a raw upstream \"status\" field")
				}
			},
		},
		{
			// A native Responses shape with an empty output array must also be
			// fully re-normalised, not passed through.
			name:       "native responses shape with empty output array is re-normalised",
			statusCode: 200,
			responseBody: `{
				"id": "resp-native-2",
				"object": "response",
				"created_at": 1677652288,
				"status": "completed",
				"model": "o1",
				"output": []
			}`,
			modelPrefix: "openai/o1",
			stream:      false,
			wantStatus:  200,
			wantErr:     false,
			validate: func(t *testing.T, result *TransformResult) {
				body, _ := io.ReadAll(result.Body)
				var raw map[string]any
				if err := json.Unmarshal(body, &raw); err != nil {
					t.Fatalf("failed to unmarshal response: %v", err)
				}
				if raw["model"] != "openai/o1" {
					t.Errorf("expected model=openai/o1, got %v", raw["model"])
				}
				if raw["id"] != "resp-native-2" {
					t.Errorf("expected id=resp-native-2, got %v", raw["id"])
				}
				output, ok := raw["output"].([]any)
				if !ok {
					t.Fatalf("expected output array, got %v", raw["output"])
				}
				if len(output) != 0 {
					t.Errorf("expected empty output array, got %d items", len(output))
				}
				if _, hasChoices := raw["choices"]; hasChoices {
					t.Error("normalised response must not contain a \"choices\" field")
				}
				if _, hasCreatedAt := raw["created_at"]; !hasCreatedAt {
					t.Error("normalised response must have a \"created_at\" field")
				}
				if _, hasCreated := raw["created"]; hasCreated {
					t.Error("normalised response must use \"created_at\", not \"created\"")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Transform(resp(tt.statusCode, tt.responseBody, nil), EndpointResponses, tt.stream, tt.modelPrefix)

			if (err != nil) != tt.wantErr {
				t.Errorf("Transform(EndpointResponses) error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if result.StatusCode != tt.wantStatus {
				t.Errorf("expected status %d, got %d", tt.wantStatus, result.StatusCode)
			}

			if tt.validate != nil {
				tt.validate(t, result)
			}
		})
	}
}

func TestMapStatusToErrorType(t *testing.T) {
	tests := []struct {
		statusCode int
		wantType   string
	}{
		{http.StatusBadRequest, "invalid_request_error"},
		{http.StatusUnauthorized, "authentication_error"},
		{http.StatusForbidden, "permission_error"},
		{http.StatusNotFound, "invalid_request_error"},
		{http.StatusTooManyRequests, "rate_limit_error"},
		{http.StatusInternalServerError, "upstream_error"},
		{http.StatusBadGateway, "upstream_error"},
		{http.StatusServiceUnavailable, "upstream_error"},
		{http.StatusGatewayTimeout, "upstream_error"},
	}

	for _, tt := range tests {
		t.Run(http.StatusText(tt.statusCode), func(t *testing.T) {
			got := mapStatusToErrorType(tt.statusCode)
			if got != tt.wantType {
				t.Errorf("mapStatusToErrorType(%d) = %s, want %s", tt.statusCode, got, tt.wantType)
			}
		})
	}
}

// TestTransformResponsesResponse_ShimPassThrough verifies that when the upstream
// backend already set Content-Type: text/event-stream on a streaming response
// (i.e. it used the responsesshim and produced Responses-API named events),
// TransformResponsesResponse forwards the body verbatim without re-encoding.
func TestTransformResponsesResponse_ShimPassThrough(t *testing.T) {
	// A pre-translated stream that carries sequence_number and logprobs —
	// fields that a re-encoding pass would silently strip.
	upstreamBody := strings.Join([]string{
		`event: response.created`,
		`data: {"type":"response.created","sequence_number":1,"response":{"id":"resp_x","object":"response","created_at":1700000000,"status":"in_progress","model":"copilot/gpt-4o","output":[]}}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","sequence_number":2,"item_id":"msg_x","output_index":0,"content_index":0,"delta":"Hi","logprobs":[]}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","sequence_number":3,"response":{"id":"resp_x","object":"response","created_at":1700000000,"status":"completed","model":"copilot/gpt-4o","output":[]}}`,
		``,
	}, "\n")

	upstreamHeader := http.Header{"Content-Type": []string{"text/event-stream"}}
	body := io.NopCloser(strings.NewReader(upstreamBody))
	upstreamResp := &backend.Response{StatusCode: 200, Header: upstreamHeader, Body: body}
	result, err := Transform(upstreamResp, EndpointResponses, true, "copilot/gpt-4o")
	if err != nil {
		t.Fatal(err)
	}
	if result.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", result.StatusCode)
	}

	out, _ := io.ReadAll(result.Body)
	outStr := string(out)

	// sequence_number and logprobs must survive verbatim.
	if !strings.Contains(outStr, `"sequence_number":1`) {
		t.Error("sequence_number:1 was stripped by transform layer")
	}
	if !strings.Contains(outStr, `"logprobs":[]`) {
		t.Error("logprobs was stripped by transform layer")
	}
	// The body must reach the client unchanged (event names preserved).
	if !strings.Contains(outStr, "response.output_text.delta") {
		t.Error("event name response.output_text.delta missing from output")
	}
}
