package mcpclient

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

// brokerFakeTool is a minimal tool.Tool for broker tests.
type brokerFakeTool struct {
	name, desc string
	params     any
	run        func(json.RawMessage) (string, error)
}

func (f brokerFakeTool) Name() string        { return f.name }
func (f brokerFakeTool) Description() string { return f.desc }
func (f brokerFakeTool) Schema() llm.ToolSchema {
	return llm.ToolSchema{Name: f.name, Description: f.desc, Parameters: f.params}
}
func (f brokerFakeTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	return f.run(raw)
}

func brokerMetaTools(t *testing.T, tools []tool.Tool) map[string]tool.Tool {
	t.Helper()
	metas, err := NewBroker(tools).Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]tool.Tool{}
	for _, mt := range metas {
		m[mt.Name()] = mt
	}
	return m
}

func TestBroker_listOmitsSchemas(t *testing.T) {
	tools := []tool.Tool{
		brokerFakeTool{name: "fs_read", desc: "read a file", params: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{}}}},
		brokerFakeTool{name: "fs_write", desc: "write a file"},
	}
	out, err := brokerMetaTools(t, tools)["mcp_list_tools"].Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "fs_read: read a file") || !strings.Contains(out, "fs_write: write a file") {
		t.Errorf("list missing entries: %q", out)
	}
	if strings.Contains(out, "properties") || strings.Contains(out, "path") {
		t.Errorf("list should NOT include schemas: %q", out)
	}
}

func TestBroker_describeReturnsSchema(t *testing.T) {
	tools := []tool.Tool{brokerFakeTool{name: "fs_read", params: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}}
	out, err := brokerMetaTools(t, tools)["mcp_describe_tool"].Execute(context.Background(), json.RawMessage(`{"name":"fs_read"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"path"`) {
		t.Errorf("describe should return params schema: %q", out)
	}
}

func TestBroker_callDispatches(t *testing.T) {
	var gotArgs string
	tools := []tool.Tool{brokerFakeTool{name: "fs_read", run: func(raw json.RawMessage) (string, error) {
		gotArgs = string(raw)
		return "file contents", nil
	}}}
	out, err := brokerMetaTools(t, tools)["mcp_call_tool"].Execute(context.Background(), json.RawMessage(`{"name":"fs_read","arguments":{"path":"/x"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "file contents" {
		t.Errorf("call output = %q", out)
	}
	if !strings.Contains(gotArgs, `"path":"/x"`) {
		t.Errorf("arguments not forwarded: %q", gotArgs)
	}
}

func TestBroker_unknownTool(t *testing.T) {
	metas := brokerMetaTools(t, nil)
	if _, err := metas["mcp_describe_tool"].Execute(context.Background(), json.RawMessage(`{"name":"nope"}`)); err == nil {
		t.Error("describe unknown should error")
	}
	if _, err := metas["mcp_call_tool"].Execute(context.Background(), json.RawMessage(`{"name":"nope"}`)); err == nil {
		t.Error("call unknown should error")
	}
}

func TestBroker_exposesThreeMetaTools(t *testing.T) {
	metas := brokerMetaTools(t, []tool.Tool{brokerFakeTool{name: "x"}})
	for _, want := range []string{"mcp_list_tools", "mcp_describe_tool", "mcp_call_tool"} {
		if _, ok := metas[want]; !ok {
			t.Errorf("missing meta-tool %q", want)
		}
	}
	if len(metas) != 3 {
		t.Errorf("expected exactly 3 meta-tools, got %d", len(metas))
	}
}
