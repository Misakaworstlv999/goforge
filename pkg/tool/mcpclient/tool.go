package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/text/unicode/norm"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

// emptyObjectSchema is used when a remote tool declares no input schema.
var emptyObjectSchema = json.RawMessage(`{"type":"object"}`)

// mcpTool adapts one remote MCP tool to GoForge's tool.Tool. Execution is
// forwarded to the MCP server via the shared session.
type mcpTool struct {
	name       string // sanitized name sent to the LLM (must match [a-zA-Z0-9_-]+)
	remoteName string // original name used in CallTool to the MCP server
	desc       string
	schema     llm.ToolSchema
	sess       session
}

// compile-time: mcpTool is a Tool.
var _ tool.Tool = (*mcpTool)(nil)

// newMCPTool converts an SDK tool descriptor into an mcpTool. The MCP input
// schema (*jsonschema.Schema) is marshaled to raw JSON and used directly as the
// LLM tool parameters; a nil schema falls back to an empty object.
//
// The LLM-facing name is prefix + the remote name, sanitized to [a-zA-Z0-9_-]+;
// a non-empty prefix namespaces tools per server to avoid collisions (e.g. two
// servers' "read_file", or an MCP "read_file" vs the builtin). The original
// remote name is preserved for CallTool.
func newMCPTool(s session, mt *mcpsdk.Tool, prefix string) (tool.Tool, error) {
	params := emptyObjectSchema
	if mt.InputSchema != nil {
		raw, err := json.Marshal(mt.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("mcp tool %q: marshal input schema: %w", mt.Name, err)
		}
		params = raw
	}
	display := mt.Name
	if prefix != "" {
		// Prefix comes from the mcpServers config key (e.g. "km-corp"). LLM
		// providers and models consistently normalize hyphens to underscores in
		// tool names, so register with underscores to match what the model calls.
		display = strings.ReplaceAll(prefix, "-", "_") + "_" + mt.Name
	}
	safeName := sanitizeToolName(display)
	return &mcpTool{
		name:       safeName,
		remoteName: mt.Name,
		desc:       mt.Description,
		sess:       s,
		schema: llm.ToolSchema{
			Name:        safeName,
			Description: mt.Description,
			Parameters:  params,
		},
	}, nil
}

func (t *mcpTool) Name() string           { return t.name }
func (t *mcpTool) Description() string    { return t.desc }
func (t *mcpTool) Schema() llm.ToolSchema { return t.schema }

// Execute forwards the call to the MCP server using the original remote name.
// The LLM-provided JSON args are passed through unchanged. A tool-level failure
// (IsError) is returned as an error so the registry encodes it into
// ToolResult.IsError for the agent.
func (t *mcpTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	args := any(raw)
	if len(raw) == 0 {
		args = nil
	}
	res, err := t.sess.CallTool(ctx, &mcpsdk.CallToolParams{Name: t.remoteName, Arguments: args})
	if err != nil {
		return "", fmt.Errorf("mcp call %q: %w", t.name, err)
	}

	text := extractText(res)
	if res.IsError {
		return "", fmt.Errorf("mcp tool %q failed: %s", t.name, text)
	}
	return text, nil
}

// toolNameRe is the pattern LLM providers (OpenAI, Anthropic/Bedrock) require.
var toolNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// sanitizeToolName makes a tool name safe for LLM APIs. If it already matches
// [a-zA-Z0-9_-]+ it is returned unchanged. Otherwise non-ASCII letters are
// transliterated to their closest ASCII decomposition (e.g. Chinese → pinyin-ish
// NFD codepoints are stripped), spaces/punctuation become underscores, and
// consecutive or trailing underscores are collapsed.
func sanitizeToolName(name string) string {
	if toolNameRe.MatchString(name) {
		return name
	}
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range norm.NFD.String(name) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		case r == '_' || r == '-':
			b.WriteRune(r)
		case unicode.Is(unicode.Mn, r):
			// combining marks from NFD decomposition — drop silently
		default:
			b.WriteByte('_')
		}
	}
	result := b.String()
	result = collapseUnderscores(result)
	result = strings.Trim(result, "_")
	if result == "" {
		result = "tool"
	}
	return result
}

func collapseUnderscores(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prev := false
	for _, r := range s {
		if r == '_' {
			if !prev {
				b.WriteByte('_')
			}
			prev = true
		} else {
			b.WriteRune(r)
			prev = false
		}
	}
	return b.String()
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
