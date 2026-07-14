package anthropic

import (
	"context"
	"encoding/json"
	"iter"
	"strings"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
	sdkanthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Config holds the configuration for the Anthropic provider.
type Config struct {
	APIKey  string
	BaseURL string
	Model   string
}

// Provider implements llm.LLM using the official anthropic-sdk-go.
type Provider struct {
	client sdkanthropic.Client
	model  string
}

// New creates a new Anthropic provider.
func New(cfg Config) *Provider {
	var opts []option.RequestOption
	if cfg.APIKey != "" {
		opts = append(opts, option.WithAPIKey(cfg.APIKey))
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}

	model := cfg.Model
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	return &Provider{
		client: sdkanthropic.NewClient(opts...),
		model:  model,
	}
}

func (p *Provider) Chat(ctx context.Context, messages []llm.Message, opts ...llm.Option) (*llm.Response, error) {
	o := llm.ApplyOptions(opts)
	params := p.buildParams(messages, o)

	msg, err := p.client.Messages.New(ctx, params)
	if err != nil {
		return nil, err
	}

	return convertMessage(msg), nil
}

func (p *Provider) ChatStream(ctx context.Context, messages []llm.Message, opts ...llm.Option) iter.Seq2[llm.Chunk, error] {
	return func(yield func(llm.Chunk, error) bool) {
		o := llm.ApplyOptions(opts)
		params := p.buildParams(messages, o)

		stream := p.client.Messages.NewStreaming(ctx, params)
		defer stream.Close()

		for stream.Next() {
			event := stream.Current()
			chunk, ok := convertStreamEvent(event)
			if !ok {
				continue
			}
			if !yield(chunk, nil) {
				return
			}
		}
		if err := stream.Err(); err != nil {
			yield(llm.Chunk{}, err)
		}
	}
}

func (p *Provider) buildParams(messages []llm.Message, o llm.Options) sdkanthropic.MessageNewParams {
	model := o.Model
	if model == "" {
		model = p.model
	}

	maxTokens := int64(o.MaxTokens)
	if maxTokens == 0 {
		maxTokens = 4096
	}

	apiMessages, systemBlocks := convertMessages(messages)

	params := sdkanthropic.MessageNewParams{
		Model:     sdkanthropic.Model(model),
		Messages:  apiMessages,
		MaxTokens: maxTokens,
	}

	if len(systemBlocks) > 0 {
		params.System = systemBlocks
	}
	if o.Temperature != nil {
		params.Temperature = sdkanthropic.Float(*o.Temperature)
	}
	if o.TopP != nil {
		params.TopP = sdkanthropic.Float(*o.TopP)
	}
	if len(o.StopSequences) > 0 {
		params.StopSequences = o.StopSequences
	}

	for _, t := range o.Tools {
		params.Tools = append(params.Tools, sdkanthropic.ToolUnionParam{
			OfTool: &sdkanthropic.ToolParam{
				Name:        t.Name,
				Description: sdkanthropic.String(t.Description),
				InputSchema: toInputSchema(t.Parameters),
			},
		})
	}

	return params
}

func convertMessages(msgs []llm.Message) ([]sdkanthropic.MessageParam, []sdkanthropic.TextBlockParam) {
	var apiMessages []sdkanthropic.MessageParam
	var systemBlocks []sdkanthropic.TextBlockParam

	for _, m := range msgs {
		switch m.Role {
		case llm.RoleSystem:
			systemBlocks = append(systemBlocks, sdkanthropic.TextBlockParam{Text: m.Content})

		case llm.RoleUser:
			apiMessages = append(apiMessages, sdkanthropic.NewUserMessage(
				sdkanthropic.NewTextBlock(m.Content),
			))

		case llm.RoleAssistant:
			var blocks []sdkanthropic.ContentBlockParamUnion
			if m.Content != "" {
				blocks = append(blocks, sdkanthropic.NewTextBlock(m.Content))
			}
			for _, tc := range m.ToolCalls {
				var input any
				_ = json.Unmarshal([]byte(tc.Args), &input)
				blocks = append(blocks, sdkanthropic.NewToolUseBlock(tc.ID, input, tc.Name))
			}
			apiMessages = append(apiMessages, sdkanthropic.NewAssistantMessage(blocks...))

		case llm.RoleTool:
			apiMessages = append(apiMessages, sdkanthropic.NewUserMessage(
				sdkanthropic.NewToolResultBlock(m.ToolCallID, m.Content, m.IsError),
			))
		}
	}

	return coalesceMessages(apiMessages), systemBlocks
}

// coalesceMessages merges consecutive same-role messages into one, concatenating
// their content blocks. The Anthropic Messages API requires strict user/assistant
// alternation, but our engine legitimately emits adjacent same-role turns: a tool
// result (mapped to a user message) followed by an injected user turn (steer,
// a resumed-conversation feedback turn, or a subagent-completion notice). Merging
// them into a single user turn holding [tool_result blocks…]+[text block] is the
// idiomatic Anthropic representation and keeps the request valid.
//
// It only ever merges WITHIN a run of the same role, so it never crosses the
// assistant turn that owns a tool_use — tool_use→tool_result adjacency and
// pairing are preserved.
func coalesceMessages(msgs []sdkanthropic.MessageParam) []sdkanthropic.MessageParam {
	if len(msgs) < 2 {
		return msgs
	}
	out := make([]sdkanthropic.MessageParam, 0, len(msgs))
	for _, m := range msgs {
		if n := len(out); n > 0 && out[n-1].Role == m.Role {
			out[n-1].Content = append(out[n-1].Content, m.Content...)
			continue
		}
		// Clone content so later appends grow this copy, never the caller's slice.
		cp := m
		cp.Content = append([]sdkanthropic.ContentBlockParamUnion(nil), m.Content...)
		out = append(out, cp)
	}
	return out
}

func convertMessage(msg *sdkanthropic.Message) *llm.Response {
	result := llm.Message{Role: llm.RoleAssistant}
	var textParts []string

	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			textParts = append(textParts, block.Text)
		case "tool_use":
			args := "{}"
			if block.Input != nil {
				args = string(block.Input)
			}
			result.ToolCalls = append(result.ToolCalls, llm.ToolCall{
				ID:   block.ID,
				Name: block.Name,
				Args: args,
			})
		}
	}
	result.Content = strings.Join(textParts, "")

	return &llm.Response{
		Message:    result,
		Usage:      convertUsage(msg.Usage),
		StopReason: convertStopReason(msg.StopReason),
	}
}

func convertStreamEvent(event sdkanthropic.MessageStreamEventUnion) (llm.Chunk, bool) {
	switch event.Type {
	case "content_block_start":
		if event.ContentBlock.Type == "tool_use" {
			return llm.Chunk{
				ToolCall: &llm.ToolCall{
					ID:   event.ContentBlock.ID,
					Name: event.ContentBlock.Name,
				},
			}, true
		}
		return llm.Chunk{}, false

	case "content_block_delta":
		switch event.Delta.Type {
		case "text_delta":
			return llm.Chunk{Delta: event.Delta.Text}, true
		case "input_json_delta":
			return llm.Chunk{
				ToolCall: &llm.ToolCall{Args: event.Delta.PartialJSON},
			}, true
		}
		return llm.Chunk{}, false

	case "message_delta":
		delta := event.AsMessageDelta()
		chunk := llm.Chunk{
			StopReason: convertStopReason(delta.Delta.StopReason),
		}
		if delta.Usage.OutputTokens > 0 {
			chunk.Usage = &llm.Usage{
				CompletionTokens: int(delta.Usage.OutputTokens),
			}
		}
		return chunk, true

	default:
		return llm.Chunk{}, false
	}
}

func convertUsage(u sdkanthropic.Usage) llm.Usage {
	return llm.Usage{
		PromptTokens:     int(u.InputTokens),
		CompletionTokens: int(u.OutputTokens),
		TotalTokens:      int(u.InputTokens + u.OutputTokens),
	}
}

func convertStopReason(reason sdkanthropic.StopReason) llm.StopReason {
	switch reason {
	case sdkanthropic.StopReasonEndTurn:
		return llm.StopReasonEnd
	case sdkanthropic.StopReasonToolUse:
		return llm.StopReasonToolCall
	case sdkanthropic.StopReasonMaxTokens:
		return llm.StopReasonMaxTokens
	default:
		return llm.StopReasonEnd
	}
}

func toInputSchema(v any) sdkanthropic.ToolInputSchemaParam {
	if v == nil {
		return sdkanthropic.ToolInputSchemaParam{}
	}

	var m map[string]any
	switch val := v.(type) {
	case map[string]any:
		m = val
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return sdkanthropic.ToolInputSchemaParam{}
		}
		if err := json.Unmarshal(data, &m); err != nil {
			return sdkanthropic.ToolInputSchemaParam{}
		}
	}

	schema := sdkanthropic.ToolInputSchemaParam{
		Properties: m["properties"],
	}
	if req, ok := m["required"].([]any); ok {
		for _, r := range req {
			if s, ok := r.(string); ok {
				schema.Required = append(schema.Required, s)
			}
		}
	}
	return schema
}
