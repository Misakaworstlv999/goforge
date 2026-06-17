package llm

// Role represents the sender of a message in a conversation.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is a provider-agnostic conversation message.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	IsError    bool       `json:"is_error,omitempty"` // tool result: true when execution failed
}

// ToolCall represents an LLM-initiated function call request.
type ToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"arguments"`
}

// ToolResult is the outcome of executing a tool call.
type ToolResult struct {
	CallID  string `json:"call_id"`
	Content string `json:"content"`
	IsError bool   `json:"is_error,omitempty"`
}

// StopReason indicates why the LLM stopped generating.
type StopReason string

const (
	StopReasonEnd       StopReason = "end"
	StopReasonToolCall  StopReason = "tool_call"
	StopReasonMaxTokens StopReason = "max_tokens"
)

// Usage tracks token consumption for a single LLM call.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Response is the complete result of a non-streaming LLM call.
type Response struct {
	Message    Message    `json:"message"`
	Usage      Usage      `json:"usage"`
	StopReason StopReason `json:"stop_reason"`
}

// Chunk is a single piece of a streaming LLM response.
type Chunk struct {
	Delta      string     `json:"delta,omitempty"`
	ToolCall   *ToolCall  `json:"tool_call,omitempty"`
	Usage      *Usage     `json:"usage,omitempty"`
	StopReason StopReason `json:"stop_reason,omitempty"`
}

// SystemMessage is a convenience constructor.
func SystemMessage(content string) Message {
	return Message{Role: RoleSystem, Content: content}
}

// UserMessage is a convenience constructor.
func UserMessage(content string) Message {
	return Message{Role: RoleUser, Content: content}
}

// AssistantMessage is a convenience constructor.
func AssistantMessage(content string) Message {
	return Message{Role: RoleAssistant, Content: content}
}

// ToolMessage creates a tool result message. Set isError when the tool failed
// so providers can mark the result as an error for the model to retry.
func ToolMessage(callID, content string, isError bool) Message {
	return Message{Role: RoleTool, Content: content, ToolCallID: callID, IsError: isError}
}
