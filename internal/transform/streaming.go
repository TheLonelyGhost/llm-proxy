// Package transform streaming functions handle Server-Sent Events (SSE)
// transformation for OpenAI-compatible streaming responses.
package transform

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
)

const maxSSEEventSize = 64 * 1024

// StreamTransformer transforms SSE events from backend responses to OpenAI format.
type StreamTransformer struct {
	modelPrefix string
	scanner     *bufio.Scanner
	objectType  string
	overflow    []byte // buffered bytes not yet consumed by the caller
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
// on-the-fly. An internal overflow buffer carries any bytes that did not fit in
// the caller's p slice across successive Read calls, so large events are
// delivered correctly regardless of buffer size. Skippable lines (malformed
// JSON, unrecognized field names) are handled with a loop rather than recursion
// so call depth is O(1) for any input.
func (st *StreamTransformer) Read(p []byte) (n int, err error) {
	// Drain any leftover bytes from a previous oversized event first.
	if len(st.overflow) > 0 {
		n = copy(p, st.overflow)
		st.overflow = st.overflow[n:]
		return n, nil
	}

	for {
		if !st.scanner.Scan() {
			if err := st.scanner.Err(); err != nil {
				return 0, err
			}
			return 0, io.EOF
		}

		event := st.scanner.Text()

		var formatted string

		switch {
		case strings.HasPrefix(event, ":"):
			// SSE comment / keep-alive — pass through verbatim.
			formatted = event + "\n\n"

		case strings.HasPrefix(event, "data: "):
			data := strings.TrimSpace(strings.TrimPrefix(event, "data: "))

			if data == "[DONE]" {
				formatted = "data: [DONE]\n\n"
				break
			}

			raw := make(map[string]any)
			if err := json.Unmarshal([]byte(data), &raw); err != nil {
				slog.Default().Debug("malformed SSE chat event, skipping", "err", err, "data_len", len(data))
				continue // skip to next scanned block
			}

			raw["model"] = st.modelPrefix
			raw["object"] = st.objectType

			transformedJSON, err := json.Marshal(raw)
			if err != nil {
				slog.Default().Debug("failed to marshal transformed SSE event, skipping", "err", err)
				continue
			}

			formatted = "data: " + string(transformedJSON) + "\n\n"

		default:
			// Unrecognized SSE field (event:, id:, retry:, empty block, etc.) — skip.
			continue
		}

		// Write as much as fits; stash the rest in overflow.
		n = copy(p, formatted)
		if n < len(formatted) {
			st.overflow = []byte(formatted[n:])
		}
		return n, nil
	}
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
	overflow    []byte // buffered bytes not yet consumed by the caller
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
	// Drain any leftover bytes from a previous oversized event first.
	if len(rt.overflow) > 0 {
		n := copy(p, rt.overflow)
		rt.overflow = rt.overflow[n:]
		return n, nil
	}

	for {
		if !rt.scanner.Scan() {
			if err := rt.scanner.Err(); err != nil {
				return 0, err
			}
			return 0, io.EOF
		}

		block := rt.scanner.Text()
		if block == "" {
			// Empty block (e.g. trailing blank lines) — skip.
			continue
		}

		var out string

		// SSE comment line — pass through verbatim.
		if strings.HasPrefix(block, ":") {
			out = block + "\n\n"
			return rt.emit(p, out)
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
			out = block + "\n\n"
			return rt.emit(p, out)
		}

		// [DONE] sentinel passes through unchanged.
		if strings.TrimSpace(dataPayload) == "[DONE]" {
			if eventLine != "" {
				out = "event: " + eventLine + "\ndata: [DONE]\n\n"
			} else {
				out = "data: [DONE]\n\n"
			}
			return rt.emit(p, out)
		}

		// Try to JSON-parse the data; if it parses, rewrite `response.model`.
		var raw map[string]any
		if err := json.Unmarshal([]byte(dataPayload), &raw); err != nil {
			slog.Default().Debug("malformed Responses SSE payload, forwarding verbatim", "err", err, "data_len", len(dataPayload))
			out = block + "\n\n"
			return rt.emit(p, out)
		}

		if resp, ok := raw["response"].(map[string]any); ok {
			if _, hasModel := resp["model"]; hasModel {
				resp["model"] = rt.modelPrefix
			}
		}

		encoded, err := json.Marshal(raw)
		if err != nil {
			slog.Default().Debug("failed to marshal transformed Responses SSE event, forwarding verbatim", "err", err)
			out = block + "\n\n"
			return rt.emit(p, out)
		}

		var sb strings.Builder
		if eventLine != "" {
			sb.WriteString("event: ")
			sb.WriteString(eventLine)
			sb.WriteByte('\n')
		}
		sb.WriteString("data: ")
		sb.Write(encoded)
		sb.WriteString("\n\n")
		return rt.emit(p, sb.String())
	}
}

// emit writes s into p. If s is larger than p, the remainder is stored in
// rt.overflow and will be drained on the next Read call.
func (rt *responsesStreamTransformer) emit(p []byte, s string) (int, error) {
	n := copy(p, s)
	if n < len(s) {
		rt.overflow = []byte(s[n:])
	}
	return n, nil
}

func (rt *responsesStreamTransformer) Close() error { return nil }
