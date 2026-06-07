package openai

import (
	"context"
	"encoding/json"
	"iter"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
	sdkopenai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

// Config holds the configuration for an OpenAI-compatible provider.
type Config struct {
	APIKey  string
	BaseURL string
	Model   string
}

// Provider implements llm.LLM using the official openai-go SDK.
type Provider struct {
	client sdkopenai.Client
	model  string
}

// New creates a new OpenAI-compatible provider.
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
		model = "gpt-5.4"
	}

	return &Provider{
		client: sdkopenai.NewClient(opts...),
		model:  model,
	}
}

func (p *Provider) Chat(ctx context.Context, messages []llm.Message, opts ...llm.Option) (*llm.Response, error) {
	o := llm.ApplyOptions(opts)
	params := p.buildParams(messages, o)

	completion, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, err
	}

	return convertCompletion(completion), nil
}

func (p *Provider) ChatStream(ctx context.Context, messages []llm.Message, opts ...llm.Option) iter.Seq2[llm.Chunk, error] {
	return func(yield func(llm.Chunk, error) bool) {
		o := llm.ApplyOptions(opts)
		params := p.buildParams(messages, o)

		stream := p.client.Chat.Completions.NewStreaming(ctx, params)
		defer stream.Close()

		for stream.Next() {
			chunk := convertStreamChunk(stream.Current())
			if !yield(chunk, nil) {
				return
			}
		}
		if err := stream.Err(); err != nil {
			yield(llm.Chunk{}, err)
		}
	}
}

func (p *Provider) buildParams(messages []llm.Message, o llm.Options) sdkopenai.ChatCompletionNewParams {
	model := o.Model
	if model == "" {
		model = p.model
	}

	params := sdkopenai.ChatCompletionNewParams{
		Model:    shared.ChatModel(model),
		Messages: convertMessages(messages),
	}

	if o.Temperature != nil {
		params.Temperature = sdkopenai.Float(*o.Temperature)
	}
	if o.MaxTokens > 0 {
		params.MaxCompletionTokens = sdkopenai.Int(int64(o.MaxTokens))
	}
	if o.TopP != nil {
		params.TopP = sdkopenai.Float(*o.TopP)
	}
	if len(o.StopSequences) > 0 {
		params.Stop = sdkopenai.ChatCompletionNewParamsStopUnion{OfStringArray: o.StopSequences}
	}

	for _, t := range o.Tools {
		params.Tools = append(params.Tools, sdkopenai.ChatCompletionToolParam{
			Function: sdkopenai.FunctionDefinitionParam{
				Name:        t.Name,
				Description: sdkopenai.String(t.Description),
				Parameters:  toFunctionParameters(t.Parameters),
			},
		})
	}

	return params
}

func convertMessages(msgs []llm.Message) []sdkopenai.ChatCompletionMessageParamUnion {
	out := make([]sdkopenai.ChatCompletionMessageParamUnion, len(msgs))
	for i, m := range msgs {
		switch m.Role {
		case llm.RoleSystem:
			out[i] = sdkopenai.SystemMessage(m.Content)
		case llm.RoleUser:
			out[i] = sdkopenai.UserMessage(m.Content)
		case llm.RoleAssistant:
			msg := sdkopenai.ChatCompletionAssistantMessageParam{
				Content: sdkopenai.ChatCompletionAssistantMessageParamContentUnion{
					OfString: sdkopenai.String(m.Content),
				},
			}
			for _, tc := range m.ToolCalls {
				msg.ToolCalls = append(msg.ToolCalls, sdkopenai.ChatCompletionMessageToolCallParam{
					ID: tc.ID,
					Function: sdkopenai.ChatCompletionMessageToolCallFunctionParam{
						Name:      tc.Name,
						Arguments: tc.Args,
					},
				})
			}
			out[i] = sdkopenai.ChatCompletionMessageParamUnion{OfAssistant: &msg}
		case llm.RoleTool:
			out[i] = sdkopenai.ToolMessage(m.ToolCallID, m.Content)
		}
	}
	return out
}

func convertCompletion(c *sdkopenai.ChatCompletion) *llm.Response {
	if len(c.Choices) == 0 {
		return &llm.Response{}
	}
	choice := c.Choices[0]
	msg := llm.Message{
		Role:    llm.RoleAssistant,
		Content: choice.Message.Content,
	}
	for _, tc := range choice.Message.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, llm.ToolCall{
			ID:   tc.ID,
			Name: tc.Function.Name,
			Args: tc.Function.Arguments,
		})
	}

	return &llm.Response{
		Message: msg,
		Usage: llm.Usage{
			PromptTokens:     int(c.Usage.PromptTokens),
			CompletionTokens: int(c.Usage.CompletionTokens),
			TotalTokens:      int(c.Usage.TotalTokens),
		},
		StopReason: convertFinishReason(string(choice.FinishReason)),
	}
}

func convertStreamChunk(chunk sdkopenai.ChatCompletionChunk) llm.Chunk {
	result := llm.Chunk{}

	if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
		result.Usage = &llm.Usage{
			PromptTokens:     int(chunk.Usage.PromptTokens),
			CompletionTokens: int(chunk.Usage.CompletionTokens),
			TotalTokens:      int(chunk.Usage.TotalTokens),
		}
	}

	if len(chunk.Choices) == 0 {
		return result
	}

	choice := chunk.Choices[0]
	result.Delta = choice.Delta.Content

	if len(choice.Delta.ToolCalls) > 0 {
		tc := choice.Delta.ToolCalls[0]
		result.ToolCall = &llm.ToolCall{
			ID:   tc.ID,
			Name: tc.Function.Name,
			Args: tc.Function.Arguments,
		}
	}

	if choice.FinishReason != "" {
		result.StopReason = convertFinishReason(string(choice.FinishReason))
	}

	return result
}

func convertFinishReason(reason string) llm.StopReason {
	switch reason {
	case "stop":
		return llm.StopReasonEnd
	case "tool_calls":
		return llm.StopReasonToolCall
	case "length":
		return llm.StopReasonMaxTokens
	default:
		return llm.StopReasonEnd
	}
}

func toFunctionParameters(v any) shared.FunctionParameters {
	if v == nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	return m
}
