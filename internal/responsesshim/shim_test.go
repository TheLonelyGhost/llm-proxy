package responsesshim

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// --- Request translator ---

func TestTranslateRequest_StringInput(t *testing.T) {
	in := []byte(`{"model":"gpt-4o","input":"Say hi","max_output_tokens":256,"temperature":0.5}`)
	out, model, stream, err := TranslateRequest(in)
	if err != nil {
		t.Fatal(err)
	}
	if model != "gpt-4o" {
		t.Errorf("model = %q, want gpt-4o", model)
	}
	if stream {
		t.Errorf("stream = true, want false")
	}
	var chat map[string]any
	if err := json.Unmarshal(out, &chat); err != nil {
		t.Fatal(err)
	}
	if chat["model"] != "gpt-4o" {
		t.Errorf("chat.model = %v", chat["model"])
	}
	if chat["max_tokens"] != float64(256) {
		t.Errorf("max_tokens = %v, want 256", chat["max_tokens"])
	}
	if chat["temperature"] != 0.5 {
		t.Errorf("temperature = %v", chat["temperature"])
	}
	msgs, _ := chat["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("len(messages) = %d, want 1", len(msgs))
	}
	m0, _ := msgs[0].(map[string]any)
	if m0["role"] != "user" || m0["content"] != "Say hi" {
		t.Errorf("first message = %+v", m0)
	}
	if _, hasUsage := chat["stream_options"]; hasUsage {
		t.Errorf("stream_options set when stream=false")
	}
}

func TestTranslateRequest_ArrayInputFlattens(t *testing.T) {
	in := []byte(`{
		"model":"gpt-4o",
		"input":[
			{"role":"system","content":[{"type":"input_text","text":"You are helpful."}]},
			{"role":"user","content":[{"type":"input_text","text":"Hi"}]}
		]
	}`)
	out, _, _, err := TranslateRequest(in)
	if err != nil {
		t.Fatal(err)
	}
	var chat map[string]any
	_ = json.Unmarshal(out, &chat)
	msgs := chat["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	if msgs[0].(map[string]any)["content"] != "You are helpful." {
		t.Errorf("system content not flattened: %+v", msgs[0])
	}
	if msgs[1].(map[string]any)["content"] != "Hi" {
		t.Errorf("user content not flattened: %+v", msgs[1])
	}
}

func TestTranslateRequest_MixedContentBecomesArray(t *testing.T) {
	in := []byte(`{
		"model":"gpt-4o",
		"input":[{
			"role":"user",
			"content":[
				{"type":"input_text","text":"What is this?"},
				{"type":"input_image","image_url":"data:image/png;base64,abc"}
			]
		}]
	}`)
	out, _, _, err := TranslateRequest(in)
	if err != nil {
		t.Fatal(err)
	}
	var chat map[string]any
	_ = json.Unmarshal(out, &chat)
	msg := chat["messages"].([]any)[0].(map[string]any)
	parts, ok := msg["content"].([]any)
	if !ok {
		t.Fatalf("content not an array: %T", msg["content"])
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(parts))
	}
	if parts[0].(map[string]any)["type"] != "text" {
		t.Errorf("first part type = %v", parts[0])
	}
	if parts[1].(map[string]any)["type"] != "image_url" {
		t.Errorf("second part type = %v", parts[1])
	}
}

func TestTranslateRequest_ToolsReshaped(t *testing.T) {
	in := []byte(`{
		"model":"gpt-4o",
		"input":"hi",
		"tools":[
			{"type":"function","name":"bash","description":"shell","parameters":{"type":"object"}},
			{"type":"web_search"}
		],
		"tool_choice":"auto"
	}`)
	out, _, _, _ := TranslateRequest(in)
	var chat map[string]any
	_ = json.Unmarshal(out, &chat)
	tools := chat["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected web_search dropped, got %d tools", len(tools))
	}
	t0 := tools[0].(map[string]any)
	if t0["type"] != "function" {
		t.Errorf("type = %v", t0["type"])
	}
	fn := t0["function"].(map[string]any)
	if fn["name"] != "bash" || fn["description"] != "shell" {
		t.Errorf("nested function bad: %+v", fn)
	}
	if chat["tool_choice"] != "auto" {
		t.Errorf("tool_choice missing: %v", chat["tool_choice"])
	}
}

func TestTranslateRequest_StreamSetsIncludeUsage(t *testing.T) {
	in := []byte(`{"model":"gpt-4o","input":"hi","stream":true}`)
	out, _, stream, _ := TranslateRequest(in)
	if !stream {
		t.Fatal("stream flag not detected")
	}
	var chat map[string]any
	_ = json.Unmarshal(out, &chat)
	so, ok := chat["stream_options"].(map[string]any)
	if !ok {
		t.Fatalf("stream_options missing: %+v", chat["stream_options"])
	}
	if so["include_usage"] != true {
		t.Errorf("include_usage = %v, want true", so["include_usage"])
	}
}

func TestTranslateRequest_InstructionsPrependSystem(t *testing.T) {
	in := []byte(`{"model":"gpt-4o","instructions":"Be brief.","input":"Hi"}`)
	out, _, _, _ := TranslateRequest(in)
	var chat map[string]any
	_ = json.Unmarshal(out, &chat)
	msgs := chat["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "Be brief." {
		t.Errorf("system not prepended: %+v", first)
	}
}

func TestTranslateRequest_FunctionCallOutputBecomesToolMessage(t *testing.T) {
	in := []byte(`{
		"model":"gpt-4o",
		"input":[
			{"role":"user","content":"run ls"},
			{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"bash","arguments":"{\"cmd\":\"ls\"}"},
			{"type":"function_call_output","call_id":"call_abc","output":"file1\nfile2"}
		]
	}`)
	out, _, _, err := TranslateRequest(in)
	if err != nil {
		t.Fatal(err)
	}
	var chat map[string]any
	_ = json.Unmarshal(out, &chat)
	msgs := chat["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3", len(msgs))
	}
	asst := msgs[1].(map[string]any)
	calls := asst["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("tool_calls = %d, want 1", len(calls))
	}
	call := calls[0].(map[string]any)
	if call["id"] != "call_abc" {
		t.Errorf("tool call id = %v", call["id"])
	}
	tool := msgs[2].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "call_abc" || tool["content"] != "file1\nfile2" {
		t.Errorf("tool message wrong: %+v", tool)
	}
}

// --- Non-streaming response translator ---

func TestTranslateChatCompletion_TextOnly(t *testing.T) {
	raw := []byte(`{
		"id":"chatcmpl-abc","object":"chat.completion","created":1700000000,"model":"gpt-4o",
		"choices":[{"index":0,"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}
	}`)
	out, err := TranslateChatCompletion(raw, "copilot/gpt-4o")
	if err != nil {
		t.Fatal(err)
	}
	var resp map[string]any
	_ = json.Unmarshal(out, &resp)
	if resp["object"] != "response" {
		t.Errorf("object = %v", resp["object"])
	}
	if resp["status"] != "completed" {
		t.Errorf("status = %v", resp["status"])
	}
	if resp["model"] != "copilot/gpt-4o" {
		t.Errorf("model = %v", resp["model"])
	}
	if resp["id"] != "resp_chatcmpl-abc" {
		t.Errorf("id = %v", resp["id"])
	}
	output := resp["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("output items = %d, want 1", len(output))
	}
	item := output[0].(map[string]any)
	if item["type"] != "message" || item["role"] != "assistant" {
		t.Errorf("item: %+v", item)
	}
	parts := item["content"].([]any)
	if parts[0].(map[string]any)["type"] != "output_text" {
		t.Errorf("content part type: %+v", parts[0])
	}
	if parts[0].(map[string]any)["text"] != "Hello!" {
		t.Errorf("content text: %+v", parts[0])
	}
	usage := resp["usage"].(map[string]any)
	if usage["input_tokens"] != float64(3) || usage["output_tokens"] != float64(2) || usage["total_tokens"] != float64(5) {
		t.Errorf("usage mismapped: %+v", usage)
	}
}

func TestTranslateChatCompletion_ToolCalls(t *testing.T) {
	raw := []byte(`{
		"id":"chatcmpl-xyz","choices":[{
			"index":0,
			"message":{"role":"assistant","content":null,"tool_calls":[
				{"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\"cmd\":\"ls\"}"}},
				{"id":"call_2","type":"function","function":{"name":"glob","arguments":"{\"pattern\":\"*.go\"}"}}
			]},
			"finish_reason":"tool_calls"
		}]
	}`)
	out, _ := TranslateChatCompletion(raw, "copilot/gpt-4o")
	var resp map[string]any
	_ = json.Unmarshal(out, &resp)
	output := resp["output"].([]any)
	if len(output) != 2 {
		t.Fatalf("output items = %d, want 2", len(output))
	}
	c0 := output[0].(map[string]any)
	if c0["type"] != "function_call" || c0["call_id"] != "call_1" || c0["name"] != "bash" {
		t.Errorf("call 0: %+v", c0)
	}
	c1 := output[1].(map[string]any)
	if c1["call_id"] != "call_2" || c1["name"] != "glob" {
		t.Errorf("call 1: %+v", c1)
	}
}

func TestTranslateChatCompletion_FinishLengthMarksIncomplete(t *testing.T) {
	raw := []byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"trunc"},"finish_reason":"length"}]}`)
	out, _ := TranslateChatCompletion(raw, "copilot/gpt-4o")
	var resp map[string]any
	_ = json.Unmarshal(out, &resp)
	if resp["status"] != "incomplete" {
		t.Errorf("status = %v, want incomplete", resp["status"])
	}
}

// --- Streaming translator ---

// readAllSSEBlocks slurps every `\n\n`-terminated block from r and returns them.
func readAllSSEBlocks(t *testing.T, r io.Reader) []string {
	t.Helper()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	scanner.Split(scanByBlankLine)
	var blocks []string
	for scanner.Scan() {
		blocks = append(blocks, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return blocks
}

func scanByBlankLine(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i := 0; i < len(data)-1; i++ {
		if data[i] == '\n' && data[i+1] == '\n' {
			return i + 2, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func blockEventName(block string) string {
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, "event:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
	}
	return ""
}

func blockData(block string) string {
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, "data:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
	return ""
}

func TestStreamTranslator_TextLifecycle(t *testing.T) {
	upstream := strings.Join([]string{
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"He"},"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"llo"},"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		``,
		`data: {"id":"chatcmpl-1","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
		``,
		`data: [DONE]`,
		``, ``,
	}, "\n")

	body := io.NopCloser(strings.NewReader(upstream))
	out := TranslateChatStreamToResponses(body, "copilot/gpt-4o")
	defer out.Close()

	blocks := readAllSSEBlocks(t, out)

	wantOrder := []string{
		"response.created",
		"response.in_progress",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.completed",
	}
	if len(blocks) != len(wantOrder) {
		t.Fatalf("got %d events, want %d\nblocks=%q", len(blocks), len(wantOrder), blocks)
	}
	for i, want := range wantOrder {
		if got := blockEventName(blocks[i]); got != want {
			t.Errorf("event %d = %q, want %q", i, got, want)
		}
	}

	// The two delta events should carry "He" and "llo".
	var deltas []string
	for _, b := range blocks {
		if blockEventName(b) != "response.output_text.delta" {
			continue
		}
		var d map[string]any
		_ = json.Unmarshal([]byte(blockData(b)), &d)
		deltas = append(deltas, d["delta"].(string))
	}
	if strings.Join(deltas, "") != "Hello" {
		t.Errorf("deltas concatenated = %q, want %q", strings.Join(deltas, ""), "Hello")
	}

	// response.completed should carry the translated usage.
	var completed map[string]any
	_ = json.Unmarshal([]byte(blockData(blocks[len(blocks)-1])), &completed)
	resp := completed["response"].(map[string]any)
	usage := resp["usage"].(map[string]any)
	if usage["input_tokens"] != float64(5) || usage["output_tokens"] != float64(2) {
		t.Errorf("usage in completed = %+v", usage)
	}
	if resp["model"] != "copilot/gpt-4o" {
		t.Errorf("model in completed = %v", resp["model"])
	}
}

func TestStreamTranslator_ToolCallDeltas(t *testing.T) {
	upstream := strings.Join([]string{
		`data: {"id":"chatcmpl-2","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_x","type":"function","function":{"name":"bash","arguments":""}}]}}]}`,
		``,
		`data: {"id":"chatcmpl-2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"cm"}}]}}]}`,
		``,
		`data: {"id":"chatcmpl-2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"d\":\"ls\"}"}}]}}]}`,
		``,
		`data: {"id":"chatcmpl-2","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		``,
		`data: [DONE]`,
		``, ``,
	}, "\n")

	body := io.NopCloser(strings.NewReader(upstream))
	out := TranslateChatStreamToResponses(body, "copilot/gpt-4o")
	defer out.Close()

	blocks := readAllSSEBlocks(t, out)
	want := []string{
		"response.created",
		"response.in_progress",
		"response.output_item.added", // function_call
		"response.function_call_arguments.delta",
		"response.function_call_arguments.delta",
		"response.function_call_arguments.done",
		"response.output_item.done",
		"response.completed",
	}
	if len(blocks) != len(want) {
		t.Fatalf("got %d events, want %d\n%q", len(blocks), len(want), blocks)
	}
	for i, w := range want {
		if got := blockEventName(blocks[i]); got != w {
			t.Errorf("event %d = %q, want %q", i, got, w)
		}
	}

	// Aggregated arguments should reconstruct the full JSON.
	var done map[string]any
	_ = json.Unmarshal([]byte(blockData(blocks[5])), &done)
	if done["arguments"] != `{"cmd":"ls"}` {
		t.Errorf("aggregated arguments = %q", done["arguments"])
	}
}
