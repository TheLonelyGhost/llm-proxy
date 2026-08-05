// Package responsesshim translates OpenAI `/v1/responses` requests to
// `/chat/completions` requests and vice-versa, so providers whose upstream
// only speaks Chat Completions can serve the Responses API without losing
// OpenAI compliance on the wire.
//
// The package is provider-agnostic: it has no knowledge of authentication,
// HMAC signing, retry, or upstream URLs. Callers (providers) are responsible
// for dispatching the translated request body through their existing
// chat-completions transport and feeding the upstream response back into the
// returned translator.
//
// Supported subset and explicit non-goals (no built-in tools, no
// previous_response_id, no retrieval) match the Copilot provider's
// `/v1/responses` shim, which was the original home of this code. See the
// per-provider docs for details.
package responsesshim

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
)

const maxSSEEventSize = 64 * 1024

// TranslateRequest converts an OpenAI /v1/responses request body into an
// OpenAI /chat/completions request body. It returns the rewritten body, the
// requested model id, and whether the caller asked for a streaming response.
// Unknown / unsupported fields (e.g. previous_response_id, store, truncation,
// include) are dropped silently.
func TranslateRequest(raw []byte) (chatBody []byte, model string, stream bool, err error) {
	var req map[string]any
	if err = json.Unmarshal(raw, &req); err != nil {
		return nil, "", false, fmt.Errorf("decode /v1/responses body: %w", err)
	}

	model, _ = req["model"].(string)
	stream, _ = req["stream"].(bool)

	chat := map[string]any{
		"model": model,
	}
	if stream {
		chat["stream"] = true
		chat["stream_options"] = map[string]any{"include_usage": true}
	}

	// Carry-through fields that have identical semantics.
	for _, k := range []string{"temperature", "top_p", "tool_choice", "metadata", "parallel_tool_calls", "user"} {
		if v, ok := req[k]; ok {
			chat[k] = v
		}
	}

	if v, ok := req["max_output_tokens"]; ok {
		chat["max_tokens"] = v
	}

	// reasoning.effort is extracted and mapped to the flat reasoning_effort
	// field that /v1/chat/completions expects. Other reasoning sub-fields
	// (summary, mode, context) have no chat/completions equivalents and are
	// dropped. The proxy has already validated the effort value.
	if v, ok := req["reasoning"]; ok {
		if obj, ok := v.(map[string]any); ok {
			if effort, ok := obj["effort"]; ok {
				chat["reasoning_effort"] = effort
			}
		}
	}

	messages, err := buildMessages(req)
	if err != nil {
		return nil, "", false, err
	}
	chat["messages"] = messages

	if tools, ok := req["tools"]; ok {
		chat["tools"] = reshapeTools(tools)
	}

	chatBody, err = json.Marshal(chat)
	if err != nil {
		return nil, "", false, fmt.Errorf("marshal chat body: %w", err)
	}
	return chatBody, model, stream, nil
}

// buildMessages flattens the OpenAI Responses `input` (and optional
// `instructions`) into a chat-completions messages array.
func buildMessages(req map[string]any) ([]map[string]any, error) {
	var messages []map[string]any

	if instr, ok := req["instructions"].(string); ok && instr != "" {
		messages = append(messages, map[string]any{
			"role":    "system",
			"content": instr,
		})
	}

	input, ok := req["input"]
	if !ok {
		return messages, nil
	}

	switch v := input.(type) {
	case string:
		messages = append(messages, map[string]any{
			"role":    "user",
			"content": v,
		})
	case []any:
		for _, item := range v {
			obj, ok := item.(map[string]any)
			if !ok {
				continue
			}
			msg, err := convertInputItem(obj)
			if err != nil {
				return nil, err
			}
			if msg != nil {
				messages = append(messages, msg)
			}
		}
	default:
		return nil, fmt.Errorf("input has unsupported type %T", input)
	}
	return messages, nil
}

// convertInputItem maps a single OpenAI Responses input item to a chat
// message. Returns (nil, nil) for items that don't map (e.g. unknown types).
func convertInputItem(item map[string]any) (map[string]any, error) {
	itemType, _ := item["type"].(string)

	// "function_call_output" feeds the tool result back to the model.
	if itemType == "function_call_output" {
		callID, _ := item["call_id"].(string)
		output, _ := item["output"].(string)
		return map[string]any{
			"role":         "tool",
			"tool_call_id": callID,
			"content":      output,
		}, nil
	}

	// "function_call" items represent an assistant-issued tool call from a
	// prior turn that the client is replaying.
	if itemType == "function_call" {
		name, _ := item["name"].(string)
		args, _ := item["arguments"].(string)
		callID, _ := item["call_id"].(string)
		id, _ := item["id"].(string)
		if callID == "" {
			callID = id
		}
		return map[string]any{
			"role": "assistant",
			"tool_calls": []map[string]any{{
				"id":   callID,
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": args,
				},
			}},
		}, nil
	}

	// Default: a role/content message. Most modern clients omit `type` and
	// send `{role, content}` directly.
	role, _ := item["role"].(string)
	if role == "" {
		role = "user"
	}
	content, err := flattenContent(item["content"])
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"role":    role,
		"content": content,
	}, nil
}

// flattenContent maps Responses content parts to chat content.
//   - nil / missing            -> empty string
//   - string                   -> string verbatim
//   - []any of input_text only -> concatenated string
//   - []any with mixed parts   -> chat content-parts array
//     ({type:"text", text:...}, {type:"image_url", image_url:{url:...}})
func flattenContent(c any) (any, error) {
	switch v := c.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	case []any:
		// Try the "all input_text" fast path so a typical user prompt remains
		// a plain string (which Copilot handles uniformly).
		allText := true
		var textBuf strings.Builder
		for _, p := range v {
			obj, ok := p.(map[string]any)
			if !ok {
				allText = false
				break
			}
			t, _ := obj["type"].(string)
			if t != "input_text" && t != "output_text" && t != "text" {
				allText = false
				break
			}
			s, _ := obj["text"].(string)
			textBuf.WriteString(s)
		}
		if allText {
			return textBuf.String(), nil
		}
		// Mixed: build a content-parts array.
		out := make([]map[string]any, 0, len(v))
		for _, p := range v {
			obj, ok := p.(map[string]any)
			if !ok {
				continue
			}
			t, _ := obj["type"].(string)
			switch t {
			case "input_text", "output_text", "text":
				if s, ok := obj["text"].(string); ok {
					out = append(out, map[string]any{"type": "text", "text": s})
				}
			case "input_image":
				if url, ok := obj["image_url"].(string); ok {
					out = append(out, map[string]any{
						"type":      "image_url",
						"image_url": map[string]any{"url": url},
					})
				} else if urlObj, ok := obj["image_url"].(map[string]any); ok {
					out = append(out, map[string]any{"type": "image_url", "image_url": urlObj})
				}
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported content type %T", c)
	}
}

// reshapeTools converts the flat Responses tool shape to the nested chat
// shape. Items that aren't function tools (e.g. {type:"web_search"}) are
// dropped because Copilot's chat surface doesn't accept them.
func reshapeTools(tools any) []map[string]any {
	arr, ok := tools.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, t := range arr {
		obj, ok := t.(map[string]any)
		if !ok {
			continue
		}
		if obj["type"] != "function" {
			continue
		}
		fn := map[string]any{}
		if v, ok := obj["name"]; ok {
			fn["name"] = v
		}
		if v, ok := obj["description"]; ok {
			fn["description"] = v
		}
		if v, ok := obj["parameters"]; ok {
			fn["parameters"] = v
		}
		// Some Responses tool definitions already use the nested shape.
		if nested, ok := obj["function"].(map[string]any); ok {
			for k, v := range nested {
				fn[k] = v
			}
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
	}
	return out
}

// TranslateChatCompletion converts a non-streaming /chat/completions JSON
// response into an OpenAI Responses JSON body.
func TranslateChatCompletion(raw []byte, modelPrefix string) ([]byte, error) {
	var chat map[string]any
	if err := json.Unmarshal(raw, &chat); err != nil {
		return nil, fmt.Errorf("decode chat completion: %w", err)
	}

	upstreamID, _ := chat["id"].(string)
	respID := "resp_" + upstreamID
	if upstreamID == "" {
		respID = fmt.Sprintf("resp_%d", time.Now().UnixNano())
	}

	created := int64(0)
	switch v := chat["created"].(type) {
	case float64:
		created = int64(v)
	case int64:
		created = v
	}
	if created == 0 {
		created = time.Now().Unix()
	}

	var (
		output []map[string]any
		status = "completed"
	)
	if choices, ok := chat["choices"].([]any); ok && len(choices) > 0 {
		choice, _ := choices[0].(map[string]any)
		if finish, _ := choice["finish_reason"].(string); finish != "" && finish != "stop" && finish != "tool_calls" && finish != "function_call" {
			if finish == "length" {
				status = "incomplete"
			}
		}
		msg, _ := choice["message"].(map[string]any)
		if msg != nil {
			if text := messageText(msg); text != "" {
				output = append(output, map[string]any{
					"type":   "message",
					"id":     "msg_" + upstreamID,
					"status": "completed",
					"role":   "assistant",
					"content": []map[string]any{{
						"type":        "output_text",
						"text":        text,
						"annotations": []any{},
					}},
				})
			}
			if calls, ok := msg["tool_calls"].([]any); ok {
				for i, c := range calls {
					call, _ := c.(map[string]any)
					if call == nil {
						continue
					}
					fn, _ := call["function"].(map[string]any)
					name, _ := fn["name"].(string)
					args, _ := fn["arguments"].(string)
					callID, _ := call["id"].(string)
					output = append(output, map[string]any{
						"type":      "function_call",
						"id":        fmt.Sprintf("fc_%s_%d", upstreamID, i),
						"call_id":   callID,
						"name":      name,
						"arguments": args,
						"status":    "completed",
					})
				}
			}
		}
	}

	resp := map[string]any{
		"id":         respID,
		"object":     "response",
		"created_at": created,
		"status":     status,
		"model":      modelPrefix,
		"output":     output,
	}
	if usage, ok := chat["usage"].(map[string]any); ok {
		resp["usage"] = translateUsage(usage)
	}

	return json.Marshal(resp)
}

func messageText(msg map[string]any) string {
	switch v := msg["content"].(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, p := range v {
			obj, ok := p.(map[string]any)
			if !ok {
				continue
			}
			if obj["type"] == "text" || obj["type"] == "output_text" {
				if s, ok := obj["text"].(string); ok {
					b.WriteString(s)
				}
			}
		}
		return b.String()
	}
	return ""
}

func translateUsage(u map[string]any) map[string]any {
	getInt := func(src map[string]any, k string) int64 {
		switch v := src[k].(type) {
		case float64:
			return int64(v)
		case int64:
			return v
		}
		return 0
	}
	// Extract reasoning_tokens from upstream completion_tokens_details if present.
	var reasoningTokens int64
	if ctd, ok := u["completion_tokens_details"].(map[string]any); ok {
		reasoningTokens = getInt(ctd, "reasoning_tokens")
	}
	// Extract cached_tokens from upstream prompt_tokens_details if present.
	var cachedTokens int64
	if ptd, ok := u["prompt_tokens_details"].(map[string]any); ok {
		cachedTokens = getInt(ptd, "cached_tokens")
	}
	return map[string]any{
		"input_tokens":  getInt(u, "prompt_tokens"),
		"output_tokens": getInt(u, "completion_tokens"),
		"total_tokens":  getInt(u, "total_tokens"),
		"input_tokens_details": map[string]any{
			"cached_tokens":      cachedTokens,
			"cache_write_tokens": int64(0),
		},
		"output_tokens_details": map[string]any{
			"reasoning_tokens": reasoningTokens,
		},
	}
}

// TranslateChatStreamToResponses wraps an upstream chat-completion SSE body
// in a reader that emits the OpenAI Responses event sequence on the fly.
func TranslateChatStreamToResponses(body io.ReadCloser, modelPrefix string) io.ReadCloser {
	t := &chatToResponsesStream{
		upstream:    body,
		modelPrefix: modelPrefix,
		scanner:     newSSEScanner(body),
		toolCalls:   map[int]*toolCallState{},
	}
	return t
}

type toolCallState struct {
	itemIndex int
	itemID    string
	callID    string
	name      string
	args      strings.Builder
	closed    bool
}

type chatToResponsesStream struct {
	upstream    io.ReadCloser
	modelPrefix string
	scanner     *bufio.Scanner

	// Outbound buffer holds the next bytes to deliver to Read().
	outbound bytes.Buffer

	respID          string // resp_<chatcmpl id>
	upstreamID      string
	created         int64
	emittedCreated  bool
	emittedItem     bool // message item added
	emittedPart     bool
	emittedComplete bool

	// Aggregated outputs for the final response.completed event.
	finalText  strings.Builder
	finalUsage map[string]any

	// nextOutputIndex tracks the index assigned to the next new item.
	nextOutputIndex int

	// messageOutputIndex is the index assigned to the assistant message item
	// (or -1 if none yet).
	messageOutputIndex int

	toolCalls map[int]*toolCallState

	finishReason string
	done         bool

	// seqNum is the monotonically-increasing sequence_number for SSE events
	// (required by the OpenAI Responses API spec on every event).
	seqNum int
}

func newSSEScanner(body io.Reader) *bufio.Scanner {
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
	return scanner
}

func (s *chatToResponsesStream) Read(p []byte) (int, error) {
	for s.outbound.Len() == 0 {
		if s.done {
			return 0, io.EOF
		}
		if err := s.fillFromUpstream(); err != nil {
			return 0, err
		}
	}
	return s.outbound.Read(p)
}

func (s *chatToResponsesStream) Close() error {
	return s.upstream.Close()
}

// fillFromUpstream consumes one upstream SSE block and writes the
// corresponding outbound Responses event(s) into s.outbound. On EOF it
// emits the trailing *.done and response.completed events.
func (s *chatToResponsesStream) fillFromUpstream() error {
	if !s.scanner.Scan() {
		if err := s.scanner.Err(); err != nil {
			return err
		}
		// EOF: drain any remaining lifecycle events.
		s.emitTerminalEvents()
		s.done = true
		return nil
	}

	block := strings.TrimSpace(s.scanner.Text())
	if block == "" {
		return nil
	}

	// Find the data: line(s). For chat completions the upstream emits just
	// `data: <json>` per block.
	var payload string
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, ":") {
			// SSE comment, ignore.
			continue
		}
		if strings.HasPrefix(line, "data:") {
			payload = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			break
		}
	}
	if payload == "" {
		return nil
	}
	if payload == "[DONE]" {
		s.emitTerminalEvents()
		s.done = true
		return nil
	}

	var chunk map[string]any
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		slog.Default().Debug("malformed upstream chat stream chunk, dropping", "err", err)
		return nil
	}

	if !s.emittedCreated {
		s.bootstrap(chunk)
	}

	// usage may appear on the final empty-choices chunk when
	// stream_options.include_usage = true.
	if u, ok := chunk["usage"].(map[string]any); ok {
		s.finalUsage = translateUsage(u)
	}

	choices, _ := chunk["choices"].([]any)
	for _, c := range choices {
		choice, _ := c.(map[string]any)
		if choice == nil {
			continue
		}
		if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
			s.finishReason = fr
		}
		delta, _ := choice["delta"].(map[string]any)
		if delta == nil {
			continue
		}
		s.handleTextDelta(delta)
		s.handleToolCallDelta(delta)
	}

	return nil
}

func (s *chatToResponsesStream) bootstrap(chunk map[string]any) {
	s.upstreamID, _ = chunk["id"].(string)
	if s.upstreamID == "" {
		s.upstreamID = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	s.respID = "resp_" + s.upstreamID
	switch v := chunk["created"].(type) {
	case float64:
		s.created = int64(v)
	case int64:
		s.created = v
	}
	if s.created == 0 {
		s.created = time.Now().Unix()
	}
	s.messageOutputIndex = -1
	s.emittedCreated = true

	s.writeEvent("response.created", map[string]any{
		"type": "response.created",
		"response": map[string]any{
			"id":         s.respID,
			"object":     "response",
			"created_at": s.created,
			"status":     "in_progress",
			"model":      s.modelPrefix,
			"output":     []any{},
		},
	})
	s.writeEvent("response.in_progress", map[string]any{
		"type": "response.in_progress",
		"response": map[string]any{
			"id":         s.respID,
			"object":     "response",
			"created_at": s.created,
			"status":     "in_progress",
			"model":      s.modelPrefix,
			"output":     []any{},
		},
	})
}

func (s *chatToResponsesStream) handleTextDelta(delta map[string]any) {
	content, ok := delta["content"].(string)
	if !ok || content == "" {
		return
	}
	if !s.emittedItem {
		s.messageOutputIndex = s.nextOutputIndex
		s.nextOutputIndex++
		s.emittedItem = true
		s.writeEvent("response.output_item.added", map[string]any{
			"type":         "response.output_item.added",
			"output_index": s.messageOutputIndex,
			"item": map[string]any{
				"type":    "message",
				"id":      "msg_" + s.upstreamID,
				"status":  "in_progress",
				"role":    "assistant",
				"content": []any{},
			},
		})
	}
	if !s.emittedPart {
		s.emittedPart = true
		s.writeEvent("response.content_part.added", map[string]any{
			"type":          "response.content_part.added",
			"item_id":       "msg_" + s.upstreamID,
			"output_index":  s.messageOutputIndex,
			"content_index": 0,
			"part": map[string]any{
				"type":        "output_text",
				"text":        "",
				"annotations": []any{},
			},
		})
	}
	s.finalText.WriteString(content)
	s.writeEvent("response.output_text.delta", map[string]any{
		"type":          "response.output_text.delta",
		"item_id":       "msg_" + s.upstreamID,
		"output_index":  s.messageOutputIndex,
		"content_index": 0,
		"delta":         content,
		"logprobs":      []any{},
	})
}

func (s *chatToResponsesStream) handleToolCallDelta(delta map[string]any) {
	calls, ok := delta["tool_calls"].([]any)
	if !ok {
		return
	}
	for _, c := range calls {
		call, _ := c.(map[string]any)
		if call == nil {
			continue
		}
		idxF, _ := call["index"].(float64)
		idx := int(idxF)
		state, exists := s.toolCalls[idx]
		if !exists {
			state = &toolCallState{
				itemIndex: s.nextOutputIndex,
				itemID:    fmt.Sprintf("fc_%s_%d", s.upstreamID, idx),
			}
			s.nextOutputIndex++
			s.toolCalls[idx] = state
		}
		if id, ok := call["id"].(string); ok && id != "" && state.callID == "" {
			state.callID = id
		}
		fn, _ := call["function"].(map[string]any)
		if fn != nil {
			if name, ok := fn["name"].(string); ok && name != "" && state.name == "" {
				state.name = name
			}
		}
		if !exists {
			s.writeEvent("response.output_item.added", map[string]any{
				"type":         "response.output_item.added",
				"output_index": state.itemIndex,
				"item": map[string]any{
					"type":      "function_call",
					"id":        state.itemID,
					"call_id":   state.callID,
					"name":      state.name,
					"arguments": "",
					"status":    "in_progress",
				},
			})
		}
		if fn != nil {
			if args, ok := fn["arguments"].(string); ok && args != "" {
				state.args.WriteString(args)
				s.writeEvent("response.function_call_arguments.delta", map[string]any{
					"type":         "response.function_call_arguments.delta",
					"item_id":      state.itemID,
					"output_index": state.itemIndex,
					"delta":        args,
				})
			}
		}
	}
}

func (s *chatToResponsesStream) emitTerminalEvents() {
	if s.emittedComplete {
		return
	}
	// Close any open tool calls first.
	for _, state := range s.toolCalls {
		if state.closed {
			continue
		}
		state.closed = true
		s.writeEvent("response.function_call_arguments.done", map[string]any{
			"type":         "response.function_call_arguments.done",
			"item_id":      state.itemID,
			"output_index": state.itemIndex,
			"name":         state.name,
			"arguments":    state.args.String(),
		})
		s.writeEvent("response.output_item.done", map[string]any{
			"type":         "response.output_item.done",
			"output_index": state.itemIndex,
			"item": map[string]any{
				"type":      "function_call",
				"id":        state.itemID,
				"call_id":   state.callID,
				"name":      state.name,
				"arguments": state.args.String(),
				"status":    "completed",
			},
		})
	}

	// Close the assistant text message, if any.
	if s.emittedItem {
		if s.emittedPart {
			s.writeEvent("response.output_text.done", map[string]any{
				"type":          "response.output_text.done",
				"item_id":       "msg_" + s.upstreamID,
				"output_index":  s.messageOutputIndex,
				"content_index": 0,
				"text":          s.finalText.String(),
				"logprobs":      []any{},
			})
			s.writeEvent("response.content_part.done", map[string]any{
				"type":          "response.content_part.done",
				"item_id":       "msg_" + s.upstreamID,
				"output_index":  s.messageOutputIndex,
				"content_index": 0,
				"part": map[string]any{
					"type":        "output_text",
					"text":        s.finalText.String(),
					"annotations": []any{},
				},
			})
		}
		s.writeEvent("response.output_item.done", map[string]any{
			"type":         "response.output_item.done",
			"output_index": s.messageOutputIndex,
			"item": map[string]any{
				"type":   "message",
				"id":     "msg_" + s.upstreamID,
				"status": "completed",
				"role":   "assistant",
				"content": []map[string]any{{
					"type":        "output_text",
					"text":        s.finalText.String(),
					"annotations": []any{},
				}},
			},
		})
	}

	status := "completed"
	if s.finishReason == "length" {
		status = "incomplete"
	}

	output := s.buildFinalOutput()
	completed := map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id":         s.respID,
			"object":     "response",
			"created_at": s.created,
			"status":     status,
			"model":      s.modelPrefix,
			"output":     output,
		},
	}
	if s.finalUsage != nil {
		completed["response"].(map[string]any)["usage"] = s.finalUsage
	}
	s.writeEvent("response.completed", completed)
	s.emittedComplete = true
}

func (s *chatToResponsesStream) buildFinalOutput() []map[string]any {
	var out []map[string]any
	if s.emittedItem {
		out = append(out, map[string]any{
			"type":   "message",
			"id":     "msg_" + s.upstreamID,
			"status": "completed",
			"role":   "assistant",
			"content": []map[string]any{{
				"type":        "output_text",
				"text":        s.finalText.String(),
				"annotations": []any{},
			}},
		})
	}
	for i := 0; ; i++ {
		state, ok := s.toolCalls[i]
		if !ok {
			break
		}
		out = append(out, map[string]any{
			"type":      "function_call",
			"id":        state.itemID,
			"call_id":   state.callID,
			"name":      state.name,
			"arguments": state.args.String(),
			"status":    "completed",
		})
	}
	if out == nil {
		out = []map[string]any{}
	}
	return out
}

func (s *chatToResponsesStream) writeEvent(name string, data map[string]any) {
	s.seqNum++
	data["sequence_number"] = s.seqNum
	encoded, err := json.Marshal(data)
	if err != nil {
		slog.Default().Debug("failed to encode Responses stream event", "event", name, "err", err)
		return
	}
	s.outbound.WriteString("event: ")
	s.outbound.WriteString(name)
	s.outbound.WriteByte('\n')
	s.outbound.WriteString("data: ")
	s.outbound.Write(encoded)
	s.outbound.WriteString("\n\n")
}
