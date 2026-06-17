package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

// emptyObjectSchema is used when a remote tool declares no input schema.
var emptyObjectSchema = json.RawMessage(`{"type":"object"}`)

// mcpTool adapts one remote MCP tool to GoForge's tool.Tool. Execution is
// forwarded to the MCP server via the shared session.
type mcpTool struct {
	name   string
	desc   string
	schema llm.ToolSchema
	sess   session
}

// compile-time: mcpTool is a Tool.
var _ tool.Tool = (*mcpTool)(nil)

// newMCPTool converts an SDK tool descriptor into an mcpTool. The MCP input
// schema (*jsonschema.Schema) is marshaled to raw JSON and used directly as the
// LLM tool parameters; a nil schema falls back to an empty object.
func newMCPTool(s session, mt *mcpsdk.Tool) (tool.Tool, error) {
	params := emptyObjectSchema
	if mt.InputSchema != nil {
		raw, err := json.Marshal(mt.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("mcp tool %q: marshal input schema: %w", mt.Name, err)
		}
		params = raw
	}
	return &mcpTool{
		name: mt.Name,
		desc: mt.Description,
		sess: s,
		schema: llm.ToolSchema{
			Name:        mt.Name,
			Description: mt.Description,
			Parameters:  params,
		},
	}, nil
}

func (t *mcpTool) Name() string           { return t.name }
func (t *mcpTool) Description() string    { return t.desc }
func (t *mcpTool) Schema() llm.ToolSchema { return t.schema }

// Execute forwards the call to the MCP server. The LLM-provided JSON args are
// passed through unchanged. A tool-level failure (IsError) is returned as an
// error so the registry encodes it into ToolResult.IsError for the agent.
func (t *mcpTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	args := any(raw)
	if len(raw) == 0 {
		args = nil
	}
	res, err := t.sess.CallTool(ctx, &mcpsdk.CallToolParams{Name: t.name, Arguments: args})
	if err != nil {
		return "", fmt.Errorf("mcp call %q: %w", t.name, err)
	}

	text := extractText(res)
	if res.IsError {
		return "", fmt.Errorf("mcp tool %q failed: %s", t.name, text)
	}
	return text, nil
}

// extractText concatenates the textual content items of a tool result.
func extractText(res *mcpsdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
