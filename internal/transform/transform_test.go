package transform

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := io.NopCloser(strings.NewReader(tt.responseBody))
			result, err := TransformChatResponse(tt.statusCode, body, tt.stream, tt.modelPrefix)

			if (err != nil) != tt.wantErr {
				t.Errorf("TransformChatResponse() error = %v, wantErr %v", err, tt.wantErr)
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
			body := io.NopCloser(strings.NewReader(tt.responseBody))
			result, err := TransformCompletionResponse(tt.statusCode, body, tt.stream, tt.modelPrefix)

			if (err != nil) != tt.wantErr {
				t.Errorf("TransformCompletionResponse() error = %v, wantErr %v", err, tt.wantErr)
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
			body := io.NopCloser(strings.NewReader(tt.responseBody))
			result, err := TransformEmbeddingResponse(tt.statusCode, body, tt.modelPrefix)

			if (err != nil) != tt.wantErr {
				t.Errorf("TransformEmbeddingResponse() error = %v, wantErr %v", err, tt.wantErr)
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := io.NopCloser(strings.NewReader(tt.responseBody))
			result, err := TransformResponsesResponse(tt.statusCode, body, tt.stream, tt.modelPrefix)

			if (err != nil) != tt.wantErr {
				t.Errorf("TransformResponsesResponse() error = %v, wantErr %v", err, tt.wantErr)
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

func TestTransformErrorResponse(t *testing.T) {
	tests := []struct {
		name         string
		statusCode   int
		responseBody string
		headers      map[string][]string
		wantStatus   int
		validate     func(t *testing.T, resp *ErrorResponse)
	}{
		{
			name:       "structured JSON error",
			statusCode: http.StatusBadRequest,
			responseBody: `{
				"error": {
					"message": "Invalid request parameter",
					"type": "invalid_request_error",
					"code": "invalid_value"
				}
			}`,
			wantStatus: http.StatusBadRequest,
			validate: func(t *testing.T, resp *ErrorResponse) {
				if resp.Error.Type != "invalid_request_error" {
					t.Errorf("expected type=invalid_request_error, got %s", resp.Error.Type)
				}
				if resp.Error.Message != "Invalid request parameter" {
					t.Errorf("unexpected message: %s", resp.Error.Message)
				}
			},
		},
		{
			name:         "plain text error",
			statusCode:   http.StatusInternalServerError,
			responseBody: "Internal server error occurred",
			wantStatus:   http.StatusInternalServerError,
			validate: func(t *testing.T, resp *ErrorResponse) {
				if resp.Error.Type != "upstream_error" {
					t.Errorf("expected type=upstream_error, got %s", resp.Error.Type)
				}
				if resp.Error.Message != "Internal server error occurred" {
					t.Errorf("unexpected message: %s", resp.Error.Message)
				}
			},
		},
		{
			name:       "error with retry-after header",
			statusCode: http.StatusTooManyRequests,
			responseBody: `{
				"error": {
					"message": "Rate limit exceeded"
				}
			}`,
			headers: map[string][]string{
				"Retry-After": {"60"},
			},
			wantStatus: http.StatusTooManyRequests,
			validate: func(t *testing.T, resp *ErrorResponse) {
				if resp.Error.Type != "rate_limit_error" {
					t.Errorf("expected type=rate_limit_error, got %s", resp.Error.Type)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := io.NopCloser(strings.NewReader(tt.responseBody))
			result, err := TransformErrorResponse(tt.statusCode, body, tt.headers)

			if err != nil {
				t.Fatalf("TransformErrorResponse() unexpected error: %v", err)
			}

			if result.StatusCode != tt.wantStatus {
				t.Errorf("expected status %d, got %d", tt.wantStatus, result.StatusCode)
			}

			bodyData, _ := io.ReadAll(result.Body)
			var errResp ErrorResponse
			if err := json.Unmarshal(bodyData, &errResp); err != nil {
				t.Fatalf("failed to unmarshal error response: %v", err)
			}

			if tt.validate != nil {
				tt.validate(t, &errResp)
			}

			if tt.headers != nil && tt.headers["Retry-After"] != nil {
				if result.Header.Get("Retry-After") != tt.headers["Retry-After"][0] {
					t.Errorf("expected Retry-After header to be preserved")
				}
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
