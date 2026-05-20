package transform

import (
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestTransformStreamingChatResponse(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		modelPrefix string
		wantEvents  []map[string]any
		wantDone    bool
	}{
		{
			name: "basic SSE chat chunk",
			input: `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"gpt-4","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

`,
			modelPrefix: "openai/gpt-4",
			wantEvents: []map[string]any{
				{
					"id":      "chatcmpl-123",
					"object":  "chat.completion.chunk",
					"created": float64(1677652288),
					"model":   "openai/gpt-4",
					"choices": []any{
						map[string]any{
							"index":         float64(0),
							"delta":         map[string]any{"content": "Hello"},
							"finish_reason": nil,
						},
					},
				},
			},
		},
		{
			name: "multiple chunks",
			input: `data: {"id":"1","choices":[{"delta":{"content":"Hello"}}]}

data: {"id":"2","choices":[{"delta":{"content":" world"}}]}

`,
			modelPrefix: "test/model",
			wantEvents: []map[string]any{
				{
					"id":      "1",
					"model":   "test/model",
					"object":  "chat.completion.chunk",
					"choices": []any{map[string]any{"delta": map[string]any{"content": "Hello"}}},
				},
				{
					"id":      "2",
					"model":   "test/model",
					"object":  "chat.completion.chunk",
					"choices": []any{map[string]any{"delta": map[string]any{"content": " world"}}},
				},
			},
		},
		{
			name: "done event",
			input: `data: [DONE]

`,
			modelPrefix: "openai/gpt-4",
			wantDone:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := io.NopCloser(strings.NewReader(tt.input))
			transformer := TransformStreamingChatResponse(body, tt.modelPrefix)
			defer transformer.Close()

			gotEvents := []map[string]any{}
			gotDone := false
			buf := make([]byte, 4096)
			for {
				n, err := transformer.Read(buf)
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("Read() error = %v", err)
				}
				if n > 0 {
					chunk := strings.TrimSpace(string(buf[:n]))
					if strings.HasPrefix(chunk, "data: ") {
						data := strings.TrimPrefix(chunk, "data: ")
						if data == "[DONE]" {
							gotDone = true
						} else {
							var event map[string]any
							if err := json.Unmarshal([]byte(data), &event); err != nil {
								t.Fatalf("failed to parse event JSON: %v", err)
							}
							gotEvents = append(gotEvents, event)
						}
					}
				}
			}

			if tt.wantDone {
				if !gotDone {
					t.Error("expected [DONE] event but did not receive it")
				}
				return
			}

			if len(gotEvents) != len(tt.wantEvents) {
				t.Fatalf("expected %d events, got %d", len(tt.wantEvents), len(gotEvents))
			}

			for i := range gotEvents {
				if !reflect.DeepEqual(gotEvents[i], tt.wantEvents[i]) {
					t.Errorf("event %d mismatch:\nwant: %#v\ngot:  %#v", i, tt.wantEvents[i], gotEvents[i])
				}
			}
		})
	}
}

func TestTransformStreamingChatResponse_KeepAlive(t *testing.T) {
	input := `: keep-alive

`
	body := io.NopCloser(strings.NewReader(input))
	transformer := TransformStreamingChatResponse(body, "test/model")
	defer transformer.Close()

	buf := make([]byte, 4096)
	n, err := transformer.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("Read() error = %v", err)
	}
	if n > 0 {
		got := string(buf[:n])
		if !strings.HasPrefix(got, ":") {
			t.Errorf("expected comment line to be preserved, got: %s", got)
		}
	}
}

func TestTransformStreamingChatResponse_MalformedJSON(t *testing.T) {
	input := `data: {invalid json

data: {"id":"2","choices":[{"delta":{"content":"ok"}}]}

`
	body := io.NopCloser(strings.NewReader(input))
	transformer := TransformStreamingChatResponse(body, "test/model")
	defer transformer.Close()

	gotEvents := []map[string]any{}
	buf := make([]byte, 4096)
	for {
		n, err := transformer.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read() error = %v", err)
		}
		if n > 0 {
			chunk := strings.TrimSpace(string(buf[:n]))
			if strings.HasPrefix(chunk, "data: ") {
				data := strings.TrimPrefix(chunk, "data: ")
				var event map[string]any
				if err := json.Unmarshal([]byte(data), &event); err == nil {
					gotEvents = append(gotEvents, event)
				}
			}
		}
	}

	if len(gotEvents) != 1 {
		t.Errorf("expected 1 valid event (malformed skipped), got %d", len(gotEvents))
	}
}

func TestTransformStreamingCompletionResponse(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		modelPrefix string
		wantEvents  []map[string]any
		wantDone    bool
	}{
		{
			name: "basic completion chunk",
			input: `data: {"id":"cmpl-123","object":"text_completion","created":1677652288,"model":"gpt-3.5-turbo-instruct","choices":[{"index":0,"text":"test","finish_reason":null}]}

`,
			modelPrefix: "openai/gpt-3.5-turbo-instruct",
			wantEvents: []map[string]any{
				{
					"id":      "cmpl-123",
					"object":  "text_completion",
					"created": float64(1677652288),
					"model":   "openai/gpt-3.5-turbo-instruct",
					"choices": []any{
						map[string]any{
							"index":         float64(0),
							"text":          "test",
							"finish_reason": nil,
						},
					},
				},
			},
		},
		{
			name: "done event",
			input: `data: [DONE]

`,
			modelPrefix: "test/model",
			wantDone:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := io.NopCloser(strings.NewReader(tt.input))
			transformer := TransformStreamingCompletionResponse(body, tt.modelPrefix)
			defer transformer.Close()

			gotEvents := []map[string]any{}
			gotDone := false
			buf := make([]byte, 4096)
			for {
				n, err := transformer.Read(buf)
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("Read() error = %v", err)
				}
				if n > 0 {
					chunk := strings.TrimSpace(string(buf[:n]))
					if strings.HasPrefix(chunk, "data: ") {
						data := strings.TrimPrefix(chunk, "data: ")
						if data == "[DONE]" {
							gotDone = true
						} else {
							var event map[string]any
							if err := json.Unmarshal([]byte(data), &event); err != nil {
								t.Fatalf("failed to parse event JSON: %v", err)
							}
							gotEvents = append(gotEvents, event)
						}
					}
				}
			}

			if tt.wantDone {
				if !gotDone {
					t.Error("expected [DONE] event but did not receive it")
				}
				return
			}

			if len(gotEvents) != len(tt.wantEvents) {
				t.Fatalf("expected %d events, got %d", len(tt.wantEvents), len(gotEvents))
			}

			for i := range gotEvents {
				if !reflect.DeepEqual(gotEvents[i], tt.wantEvents[i]) {
					t.Errorf("event %d mismatch:\nwant: %#v\ngot:  %#v", i, tt.wantEvents[i], gotEvents[i])
				}
			}
		})
	}
}

func TestTransformStreamingResponsesResponse(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		modelPrefix string
		wantEvents  []map[string]any
		wantDone    bool
	}{
		{
			name: "reasoning chunk",
			input: `data: {"id":"resp-123","object":"response.chunk","created":1677652288,"model":"o1-preview","choices":[{"index":0,"delta":{"content":"thinking...","reasoning":"analyzing"},"finish_reason":null}]}

`,
			modelPrefix: "openai/o1-preview",
			wantEvents: []map[string]any{
				{
					"id":      "resp-123",
					"object":  "response.chunk",
					"created": float64(1677652288),
					"model":   "openai/o1-preview",
					"choices": []any{
						map[string]any{
							"index": float64(0),
							"delta": map[string]any{
								"content":   "thinking...",
								"reasoning": "analyzing",
							},
							"finish_reason": nil,
						},
					},
				},
			},
		},
		{
			name: "done event",
			input: `data: [DONE]

`,
			modelPrefix: "openai/o1",
			wantDone:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := io.NopCloser(strings.NewReader(tt.input))
			transformer := TransformStreamingResponsesResponse(body, tt.modelPrefix)
			defer transformer.Close()

			gotEvents := []map[string]any{}
			gotDone := false
			buf := make([]byte, 4096)
			for {
				n, err := transformer.Read(buf)
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("Read() error = %v", err)
				}
				if n > 0 {
					chunk := strings.TrimSpace(string(buf[:n]))
					if strings.HasPrefix(chunk, "data: ") {
						data := strings.TrimPrefix(chunk, "data: ")
						if data == "[DONE]" {
							gotDone = true
						} else {
							var event map[string]any
							if err := json.Unmarshal([]byte(data), &event); err != nil {
								t.Fatalf("failed to parse event JSON: %v", err)
							}
							gotEvents = append(gotEvents, event)
						}
					}
				}
			}

			if tt.wantDone {
				if !gotDone {
					t.Error("expected [DONE] event but did not receive it")
				}
				return
			}

			if len(gotEvents) != len(tt.wantEvents) {
				t.Fatalf("expected %d events, got %d", len(tt.wantEvents), len(gotEvents))
			}

			for i := range gotEvents {
				if !reflect.DeepEqual(gotEvents[i], tt.wantEvents[i]) {
					t.Errorf("event %d mismatch:\nwant: %#v\ngot:  %#v", i, tt.wantEvents[i], gotEvents[i])
				}
			}
		})
	}
}

func TestStreamTransformer_EmptyStream(t *testing.T) {
	body := io.NopCloser(strings.NewReader(""))
	transformer := TransformStreamingChatResponse(body, "test/model")
	defer transformer.Close()

	buf := make([]byte, 4096)
	n, err := transformer.Read(buf)
	if err != io.EOF {
		t.Errorf("expected EOF for empty stream, got: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 bytes read, got %d", n)
	}
}

func TestStreamTransformer_MultipleReads(t *testing.T) {
	input := `data: {"id":"1","choices":[{"delta":{"content":"A"}}]}

data: {"id":"2","choices":[{"delta":{"content":"B"}}]}

data: {"id":"3","choices":[{"delta":{"content":"C"}}]}

`
	body := io.NopCloser(strings.NewReader(input))
	transformer := TransformStreamingChatResponse(body, "test/model")
	defer transformer.Close()

	chunks := 0
	buf := make([]byte, 4096)
	for {
		n, err := transformer.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read() error = %v", err)
		}
		if n > 0 {
			chunks++
		}
	}

	if chunks != 3 {
		t.Errorf("expected 3 chunks, got %d", chunks)
	}
}

func BenchmarkStreamTransformer(b *testing.B) {
	chunk := `data: {"id":"test","object":"chat.completion.chunk","choices":[{"delta":{"content":"x"},"finish_reason":null}]}

`
	var input strings.Builder
	for i := 0; i < 1000; i++ {
		input.WriteString(chunk)
	}
	inputStr := input.String()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		body := io.NopCloser(strings.NewReader(inputStr))
		transformer := TransformStreamingChatResponse(body, "test/model")

		buf := make([]byte, 4096)
		for {
			_, err := transformer.Read(buf)
			if err == io.EOF {
				break
			}
			if err != nil {
				b.Fatalf("Read() error = %v", err)
			}
		}
		transformer.Close()
	}
}

func BenchmarkStreamTransformer_LargeStream(b *testing.B) {
	chunk := `data: {"id":"test","object":"chat.completion.chunk","choices":[{"delta":{"content":"Lorem ipsum dolor sit amet, consectetur adipiscing elit."},"finish_reason":null}]}

`
	chunkSize := len(chunk)
	chunksNeeded := (2 * 1024 * 1024) / chunkSize

	var input strings.Builder
	for i := 0; i < chunksNeeded; i++ {
		input.WriteString(chunk)
	}
	inputStr := input.String()

	b.Logf("Testing with stream size: %d bytes (%d chunks)", len(inputStr), chunksNeeded)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		body := io.NopCloser(strings.NewReader(inputStr))
		transformer := TransformStreamingChatResponse(body, "test/model")

		buf := make([]byte, 4096)
		totalBytes := 0
		for {
			n, err := transformer.Read(buf)
			if err == io.EOF {
				break
			}
			if err != nil {
				b.Fatalf("Read() error = %v", err)
			}
			totalBytes += n
		}
		transformer.Close()

		if i == 0 {
			b.Logf("Processed %d bytes", totalBytes)
		}
	}
}

func TestStreamTransformer_NoBuffering(t *testing.T) {
	chunk := `data: {"id":"test","choices":[{"delta":{"content":"x"}}]}

`
	chunkSize := len(chunk)
	chunksNeeded := (2 * 1024 * 1024) / chunkSize

	var input strings.Builder
	for i := 0; i < chunksNeeded; i++ {
		input.WriteString(chunk)
	}

	body := io.NopCloser(strings.NewReader(input.String()))
	transformer := TransformStreamingChatResponse(body, "test/model")
	defer transformer.Close()

	buf := make([]byte, 4096)
	firstReadBytes := 0
	n, err := transformer.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("First Read() error = %v", err)
	}
	firstReadBytes = n

	if firstReadBytes == 0 {
		t.Fatal("First read returned 0 bytes, expected immediate data")
	}

	t.Logf("First read returned %d bytes (stream is not fully buffered)", firstReadBytes)
}
