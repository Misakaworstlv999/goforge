package telemetry

import (
	"fmt"
	"strings"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// BodyCapture controls whether LLM/tool request/response previews are attached
// to spans exported to Jaeger. Off is the zero-value default (no overhead).
type BodyCapture int

const (
	BodyOff BodyCapture = iota
	BodyPreview
	BodyFull
)

const (
	defaultBodyMaxBytes = 2048
	fullBodyMaxBytes    = 256 * 1024
	attrGenAIPrompt     = "gen_ai.prompt"
	attrGenAICompletion = "gen_ai.completion"
	attrToolArgsPreview = "goforge.tool.args_preview"
	attrToolResult      = "goforge.tool.result_preview"
)

var (
	bodyCapture  BodyCapture
	bodyMaxBytes = defaultBodyMaxBytes
)

// ParseBodyCapture parses GOFORGE_OTEL_BODY values: off | preview | full.
func ParseBodyCapture(s string) (BodyCapture, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "off", "false", "0":
		return BodyOff, nil
	case "preview", "on", "true", "1":
		return BodyPreview, nil
	case "full":
		return BodyFull, nil
	default:
		return BodyOff, fmt.Errorf("unknown body capture mode %q (want off, preview, or full)", s)
	}
}

func configureBodyCapture(mode BodyCapture, maxBytes int) {
	bodyCapture = mode
	if maxBytes > 0 {
		bodyMaxBytes = maxBytes
	} else {
		bodyMaxBytes = defaultBodyMaxBytes
	}
}

func clipBody(s string) string {
	if bodyCapture == BodyOff {
		return ""
	}
	max := bodyMaxBytes
	if bodyCapture == BodyFull && max <= defaultBodyMaxBytes {
		max = fullBodyMaxBytes
	}
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// RecordLLMExchange attaches truncated request/response previews to an LLM span.
func RecordLLMExchange(span trace.Span, req []llm.Message, resp llm.Message) {
	if bodyCapture == BodyOff || !span.IsRecording() {
		return
	}
	span.SetAttributes(
		attribute.String(attrGenAIPrompt, clipBody(formatMessages(req))),
		attribute.String(attrGenAICompletion, clipBody(formatAssistant(resp))),
	)
}

// RecordToolExchange attaches truncated tool args/results to a tool-batch span.
func RecordToolExchange(span trace.Span, calls []llm.ToolCall, results []llm.ToolResult) {
	if bodyCapture == BodyOff || !span.IsRecording() {
		return
	}
	span.SetAttributes(
		attribute.String(attrToolArgsPreview, clipBody(formatToolCalls(calls))),
		attribute.String(attrToolResult, clipBody(formatToolResults(results))),
	)
}

func formatMessages(msgs []llm.Message) string {
	if len(msgs) == 0 {
		return ""
	}
	var b strings.Builder
	for _, m := range msgs {
		fmt.Fprintf(&b, "[%s] ", m.Role)
		if m.Content != "" {
			b.WriteString(m.Content)
		}
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(&b, " tool_call %s(%s)", tc.Name, tc.Args)
		}
		if m.ToolCallID != "" {
			fmt.Fprintf(&b, " (tool_result for %s)", m.ToolCallID)
		}
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

func formatAssistant(m llm.Message) string {
	var b strings.Builder
	if m.Content != "" {
		b.WriteString(m.Content)
	}
	for _, tc := range m.ToolCalls {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "tool_call %s(%s)", tc.Name, tc.Args)
	}
	return b.String()
}

func formatToolCalls(calls []llm.ToolCall) string {
	var parts []string
	for _, c := range calls {
		parts = append(parts, fmt.Sprintf("%s(%s)", c.Name, c.Args))
	}
	return strings.Join(parts, "; ")
}

func formatToolResults(results []llm.ToolResult) string {
	var b strings.Builder
	for i, r := range results {
		if i > 0 {
			b.WriteString(" | ")
		}
		if r.IsError {
			b.WriteString("[error] ")
		}
		b.WriteString(r.Content)
	}
	return b.String()
}
