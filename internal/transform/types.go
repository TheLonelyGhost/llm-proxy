package transform

import "time"

// ChatCompletionResponse represents an OpenAI chat completion response.
type ChatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []ChatCompletionChoice `json:"choices"`
	Usage   *Usage                 `json:"usage,omitempty"`
}

// ChatCompletionChoice represents a single choice in a chat completion response.
type ChatCompletionChoice struct {
	Index        int       `json:"index"`
	Message      *Message  `json:"message,omitempty"`
	Delta        *Message  `json:"delta,omitempty"`
	FinishReason *string   `json:"finish_reason"`
	Logprobs     *Logprobs `json:"logprobs,omitempty"`
}

// ToolCallFunction holds the function name and arguments for a tool call.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolCall represents a single tool call issued by the model.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

// Message represents a chat message with role, content, and optional tool calls.
type Message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// CompletionResponse represents an OpenAI text completion response.
type CompletionResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []CompletionChoice `json:"choices"`
	Usage   *Usage             `json:"usage,omitempty"`
}

// CompletionChoice represents a single choice in a text completion response.
type CompletionChoice struct {
	Index        int       `json:"index"`
	Text         string    `json:"text"`
	Logprobs     *Logprobs `json:"logprobs"`
	FinishReason *string   `json:"finish_reason"`
}

// Logprobs contains log probability information for tokens.
type Logprobs struct {
	Tokens        []string             `json:"tokens,omitempty"`
	TokenLogprobs []float64            `json:"token_logprobs,omitempty"`
	TopLogprobs   []map[string]float64 `json:"top_logprobs,omitempty"`
	TextOffset    []int                `json:"text_offset,omitempty"`
}

// EmbeddingResponse represents an OpenAI embedding response.
type EmbeddingResponse struct {
	Object string          `json:"object"`
	Data   []EmbeddingData `json:"data"`
	Model  string          `json:"model"`
	Usage  *Usage          `json:"usage,omitempty"`
}

// EmbeddingData represents a single embedding vector.
type EmbeddingData struct {
	Object    string    `json:"object"`
	Index     int       `json:"index"`
	Embedding []float64 `json:"embedding"`
}

// ResponsesResponse represents an OpenAI unified response object.
type ResponsesResponse struct {
	ID      string            `json:"id"`
	Object  string            `json:"object"`
	Created int64             `json:"created"`
	Model   string            `json:"model"`
	Choices []ResponsesChoice `json:"choices"`
	Usage   *Usage            `json:"usage,omitempty"`
}

// ResponsesChoice represents a single choice in a responses endpoint response.
type ResponsesChoice struct {
	Index        int      `json:"index"`
	Message      *Message `json:"message,omitempty"`
	Delta        *Message `json:"delta,omitempty"`
	FinishReason *string  `json:"finish_reason"`
}

// Usage contains token usage information.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens,omitempty"`
}

// ErrorResponse represents an OpenAI error response.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail contains the error information.
type ErrorDetail struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Code    *string `json:"code"`
	Param   *string `json:"param,omitempty"`
}

func now() int64 {
	return time.Now().Unix()
}
