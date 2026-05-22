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
// OpenAI /v1/responses events. Unlike the chat-completions stream (single
// `data:` lines whose payload has top-level `model`/`object`), the Responses
// stream is a sequence of NAMED SSE events of the form:
//
//	event: response.created
//	data: {"type":"response.created","response":{...}}
//
//	event: response.output_text.delta
//	data: {"type":"response.output_text.delta","delta":"H",...}
//
// The transformer preserves the `event:` lines and the `data:` JSON payload
// structure verbatim, only rewriting `response.model` (when present) to the
// backend-prefixed form so clients see the same model id they requested.
// `data: [DONE]` sentinels and SSE comment lines (`:keep-alive`) pass through
// unchanged.
func TransformStreamingResponsesResponse(body io.ReadCloser, modelPrefix string) io.ReadCloser {
	transformer := newResponsesStreamTransformer(body, modelPrefix)
	return &streamingReader{
		transformer:  transformer,
		originalBody: body,
	}
}

// responsesStreamTransformer is an SSE reader specialised for OpenAI's
// /v1/responses event format. See TransformStreamingResponsesResponse.
type responsesStreamTransformer struct {
	modelPrefix string
	scanner     *bufio.Scanner
}

func newResponsesStreamTransformer(body io.ReadCloser, modelPrefix string) *responsesStreamTransformer {
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
	return &responsesStreamTransformer{modelPrefix: modelPrefix, scanner: scanner}
}

func (rt *responsesStreamTransformer) Read(p []byte) (int, error) {
	if !rt.scanner.Scan() {
		if err := rt.scanner.Err(); err != nil {
			return 0, err
		}
		return 0, io.EOF
	}

	block := rt.scanner.Text()
	if block == "" {
		// Empty block (e.g. trailing blank lines) — skip and try the next.
		return rt.Read(p)
	}

	// SSE comment line — pass through verbatim.
	if strings.HasPrefix(block, ":") {
		return rt.write(p, block+"\n\n")
	}

	var eventLine string
	var dataPayload string
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "event:"):
			eventLine = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			// SSE permits a single space after the colon.
			payload := strings.TrimPrefix(line, "data:")
			payload = strings.TrimPrefix(payload, " ")
			if dataPayload == "" {
				dataPayload = payload
			} else {
				// Multi-line `data:` continuation: SSE concatenates with \n.
				dataPayload += "\n" + payload
			}
		}
	}

	// No data field at all — pass the block through verbatim (best-effort).
	if dataPayload == "" {
		return rt.write(p, block+"\n\n")
	}

	// [DONE] sentinel passes through unchanged.
	if strings.TrimSpace(dataPayload) == "[DONE]" {
		out := "data: [DONE]\n\n"
		if eventLine != "" {
			out = "event: " + eventLine + "\n" + out
		}
		return rt.write(p, out)
	}

	// Try to JSON-parse the data; if it parses, rewrite `response.model`.
	var raw map[string]any
	if err := json.Unmarshal([]byte(dataPayload), &raw); err != nil {
		// Malformed payload — forward as-is so we don't silently drop it.
		return rt.write(p, block+"\n\n")
	}

	if resp, ok := raw["response"].(map[string]any); ok {
		if _, hasModel := resp["model"]; hasModel {
			resp["model"] = rt.modelPrefix
		}
	}

	encoded, err := json.Marshal(raw)
	if err != nil {
		return rt.write(p, block+"\n\n")
	}

	var out strings.Builder
	if eventLine != "" {
		out.WriteString("event: ")
		out.WriteString(eventLine)
		out.WriteByte('\n')
	}
	out.WriteString("data: ")
	out.Write(encoded)
	out.WriteString("\n\n")
	return rt.write(p, out.String())
}

func (rt *responsesStreamTransformer) write(p []byte, s string) (int, error) {
	if len(s) > len(p) {
		return 0, fmt.Errorf("buffer too small for SSE block (%d bytes)", len(s))
	}
	return copy(p, s), nil
}

func (rt *responsesStreamTransformer) Close() error { return nil }
