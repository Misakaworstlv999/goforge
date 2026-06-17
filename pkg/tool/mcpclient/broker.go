package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

// Broker implements progressive disclosure for large MCP toolsets. Instead of
// registering every remote tool (dumping all their schemas into the system
// prompt), it indexes the tools and exposes just three meta-tools, so the model
// discovers and invokes tools on demand:
//
//	mcp_list_tools     {}                     -> name: description (no schemas)
//	mcp_describe_tool  {name}                 -> that tool's JSON-schema parameters
//	mcp_call_tool      {name, arguments}      -> runs the tool, returns its output
//
// It is transport-agnostic: it indexes any []tool.Tool by name (the tools come
// from one or more connected Clients), reusing their Execute/Schema.
type Broker struct {
	byName map[string]tool.Tool
	names  []string // sorted, for stable listing
}

// NewBroker indexes the given tools by name. Later tools with a duplicate name
// are skipped (first wins) — callers should prefix names per server to avoid it.
func NewBroker(tools []tool.Tool) *Broker {
	b := &Broker{byName: make(map[string]tool.Tool, len(tools))}
	for _, t := range tools {
		if _, exists := b.byName[t.Name()]; exists {
			continue
		}
		b.byName[t.Name()] = t
		b.names = append(b.names, t.Name())
	}
	sort.Strings(b.names)
	return b
}

// compile-time: Broker is a ToolSet.
var _ tool.ToolSet = (*Broker)(nil)

// Tools returns the three meta-tools. Implements tool.ToolSet.
func (b *Broker) Tools(context.Context) ([]tool.Tool, error) {
	return []tool.Tool{b.listTool(), b.describeTool(), b.callTool()}, nil
}

type describeArgs struct {
	Name string `json:"name" jsonschema:"description=Exact tool name from mcp_list_tools,required"`
}

type callArgs struct {
	Name      string          `json:"name" jsonschema:"description=Exact tool name from mcp_list_tools,required"`
	Arguments json.RawMessage `json:"arguments,omitempty" jsonschema:"description=JSON arguments object for the tool"`
}

func (b *Broker) listTool() tool.Tool {
	return tool.NewTool("mcp_list_tools",
		"List the available MCP tools as 'name: description' lines (no parameter schemas). Call mcp_describe_tool to get a tool's parameters before calling it.",
		func(context.Context, struct{}) (string, error) {
			if len(b.names) == 0 {
				return "(no MCP tools available)", nil
			}
			var sb strings.Builder
			for _, n := range b.names {
				fmt.Fprintf(&sb, "%s: %s\n", n, b.byName[n].Description())
			}
			return strings.TrimRight(sb.String(), "\n"), nil
		})
}

func (b *Broker) describeTool() tool.Tool {
	return tool.NewTool("mcp_describe_tool",
		"Get the JSON-schema parameters for a named MCP tool (from mcp_list_tools).",
		func(_ context.Context, a describeArgs) (string, error) {
			t, ok := b.byName[a.Name]
			if !ok {
				return "", fmt.Errorf("unknown tool %q (use mcp_list_tools)", a.Name)
			}
			raw, err := json.Marshal(t.Schema().Parameters)
			if err != nil {
				return "", fmt.Errorf("marshal schema for %q: %w", a.Name, err)
			}
			return string(raw), nil
		})
}

func (b *Broker) callTool() tool.Tool {
	return tool.NewTool("mcp_call_tool",
		"Call a named MCP tool (from mcp_list_tools) with a JSON arguments object.",
		func(ctx context.Context, a callArgs) (string, error) {
			t, ok := b.byName[a.Name]
			if !ok {
				return "", fmt.Errorf("unknown tool %q (use mcp_list_tools)", a.Name)
			}
			return t.Execute(ctx, a.Arguments)
		})
}
