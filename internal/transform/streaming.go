// Package transform streaming functions handle Server-Sent Events (SSE)
// transformation for OpenAI-compatible streaming responses.
package transform

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const maxSSEEventSize = 64 * 1024

// StreamTransformer transforms SSE events from backend responses to OpenAI format.
type StreamTransformer struct {
	modelPrefix string
	scanner     *bufio.Scanner
	objectType  string
}

// NewStreamTransformer creates a new SSE stream transformer that rewrites
// model and object fields in streaming events while preserving SSE structure.
func NewStreamTransformer(body io.ReadCloser, modelPrefix string, objectType string) *StreamTransformer {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), maxSSEEventSize)

	scanner.Split(func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		if atEOF && len(data) == 0 {
			return 0, nil, nil
		}

		if i := bytes.Index(data, []byte("\n\n")); i >= 0 {
			return i + 2, data[0:i], nil
		}

		if i := bytes.Index(data, []byte("\r\n\r\n")); i >= 0 {
			return i + 4, data[0:i], nil
		}

		if atEOF {
			return len(data), data, nil
		}

		return 0, nil, nil
	})

	return &StreamTransformer{
		modelPrefix: modelPrefix,
		scanner:     scanner,
		objectType:  objectType,
	}
}

// Read implements io.Reader for the StreamTransformer, transforming SSE events
// on-the-fly without buffering. Handles [DONE] events, comment lines for keep-alive,
// and malformed JSON events (which are skipped).
func (st *StreamTransformer) Read(p []byte) (n int, err error) {
	if !st.scanner.Scan() {
		if err := st.scanner.Err(); err != nil {
			return 0, err
		}
		return 0, io.EOF
	}

	event := st.scanner.Text()

	if strings.HasPrefix(event, ":") {
		formatted := event + "\n\n"
		n = copy(p, formatted)
		if n < len(formatted) {
			return n, fmt.Errorf("buffer too small for SSE comment")
		}
		return n, nil
	}

	if !strings.HasPrefix(event, "data: ") {
		return 0, nil
	}

	data := strings.TrimPrefix(event, "data: ")
	data = strings.TrimSpace(data)

	if data == "[DONE]" {
		formatted := "data: [DONE]\n\n"
		n = copy(p, formatted)
		return n, nil
	}

	raw := make(map[string]any)
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		// Skip malformed events rather than failing the stream
		return st.Read(p)
	}

	raw["model"] = st.modelPrefix
	raw["object"] = st.objectType

	transformed, err := json.Marshal(raw)
	if err != nil {
		// Skip events that can't be marshaled
		return st.Read(p)
	}

	formatted := "data: " + string(transformed) + "\n\n"
	n = copy(p, formatted)
	if n < len(formatted) {
		return n, fmt.Errorf("buffer too small for SSE event")
	}

	return n, nil
}

// Close implements io.Closer for the StreamTransformer.
func (st *StreamTransformer) Close() error {
	return nil
}

type streamingReader struct {
	transformer  io.ReadCloser
	originalBody io.ReadCloser
}

func (sr *streamingReader) Read(p []byte) (n int, err error) {
	return sr.transformer.Read(p)
}

func (sr *streamingReader) Close() error {
	_ = sr.originalBody.Close()
	return sr.transformer.Close()
}

// TransformStreamingChatResponse creates an SSE stream transformer for
// chat completion chunks, setting the object type to "chat.completion.chunk".
func TransformStreamingChatResponse(body io.ReadCloser, modelPrefix string) io.ReadCloser {
	transformer := NewStreamTransformer(body, modelPrefix, "chat.completion.chunk")
	return &streamingReader{
		transformer:  transformer,
		originalBody: body,
	}
}

// TransformStreamingCompletionResponse creates an SSE stream transformer for
// text completion chunks, setting the object type to "text_completion".
func TransformStreamingCompletionResponse(body io.ReadCloser, modelPrefix string) io.ReadCloser {
	transformer := NewStreamTransformer(body, modelPrefix, "text_completion")
	return &streamingReader{
		transformer:  transformer,
		originalBody: body,
	}
}

// TransformStreamingResponsesResponse creates an SSE stream transformer for
// responses endpoint chunks with reasoning deltas, setting the object type to "response.chunk".
func TransformStreamingResponsesResponse(body io.ReadCloser, modelPrefix string) io.ReadCloser {
	transformer := NewStreamTransformer(body, modelPrefix, "response.chunk")
	return &streamingReader{
		transformer:  transformer,
		originalBody: body,
	}
}
