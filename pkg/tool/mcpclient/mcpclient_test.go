package mcpclient

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

// ---- unit tests with a fake session ----

type fakeSession struct {
	pages  map[string]*mcpsdk.ListToolsResult // keyed by incoming cursor
	callFn func(*mcpsdk.CallToolParams) (*mcpsdk.CallToolResult, error)
	closed bool
}

func (f *fakeSession) ListTools(_ context.Context, p *mcpsdk.ListToolsParams) (*mcpsdk.ListToolsResult, error) {
	return f.pages[p.Cursor], nil
}

func (f *fakeSession) CallTool(_ context.Context, p *mcpsdk.CallToolParams) (*mcpsdk.CallToolResult, error) {
	return f.callFn(p)
}

func (f *fakeSession) Close() error { f.closed = true; return nil }

func TestTools_paginationAndSchema(t *testing.T) {
	fs := &fakeSession{pages: map[string]*mcpsdk.ListToolsResult{
		"": {
			Tools:      []*mcpsdk.Tool{{Name: "withschema", Description: "d1", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "string"}}}}},
			NextCursor: "p2",
		},
		"p2": {
			Tools: []*mcpsdk.Tool{{Name: "noschema", Description: "d2"}}, // nil InputSchema
		},
	}}

	tools, err := newClientWith(fs).Tools(context.Background())
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("got %d tools across pages, want 2", len(tools))
	}

	byName := map[string]string{}
	for _, tl := range tools {
		raw, _ := json.Marshal(tl.Schema().Parameters)
		byName[tl.Name()] = string(raw)
	}
	if !strings.Contains(byName["withschema"], `"properties"`) {
		t.Errorf("withschema params lost: %s", byName["withschema"])
	}
	if byName["noschema"] != `{"type":"object"}` {
		t.Errorf("noschema should fall back to empty object, got %s", byName["noschema"])
	}
}

func TestExecute_textAndError(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		fs := &fakeSession{
			pages: map[string]*mcpsdk.ListToolsResult{"": {Tools: []*mcpsdk.Tool{{Name: "echo"}}}},
			callFn: func(p *mcpsdk.CallToolParams) (*mcpsdk.CallToolResult, error) {
				return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "hello"}}}, nil
			},
		}
		tools, _ := newClientWith(fs).Tools(context.Background())
		out, err := tools[0].Execute(context.Background(), json.RawMessage(`{"m":"hi"}`))
		if err != nil || out != "hello" {
			t.Errorf("out=%q err=%v, want hello/nil", out, err)
		}
	})

	t.Run("is_error", func(t *testing.T) {
		fs := &fakeSession{
			pages: map[string]*mcpsdk.ListToolsResult{"": {Tools: []*mcpsdk.Tool{{Name: "boom"}}}},
			callFn: func(p *mcpsdk.CallToolParams) (*mcpsdk.CallToolResult, error) {
				return &mcpsdk.CallToolResult{IsError: true, Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "bad input"}}}, nil
			},
		}
		tools, _ := newClientWith(fs).Tools(context.Background())
		_, err := tools[0].Execute(context.Background(), json.RawMessage(`{}`))
		if err == nil || !strings.Contains(err.Error(), "bad input") {
			t.Errorf("expected IsError surfaced as error, got %v", err)
		}
	})
}

func TestSanitizeToolName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"calculator", "calculator"},
		{"get-issues", "get-issues"},
		{"KM_文档解析", "KM"},
		{"hello world", "hello_world"},
		{"a--b__c", "a--b__c"},
		{"  spaced  ", "spaced"},
		{"café", "cafe"},
		{"工具A", "A"},
		{"", "tool"},
		{"___", "___"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeToolName(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeToolName(%q) = %q, want %q", tt.input, got, tt.want)
			}
			if !toolNameRe.MatchString(got) {
				t.Errorf("sanitizeToolName(%q) = %q, does not match [a-zA-Z0-9_-]+", tt.input, got)
			}
		})
	}
}

func TestExecute_usesRemoteName(t *testing.T) {
	var calledWith string
	fs := &fakeSession{
		pages: map[string]*mcpsdk.ListToolsResult{"": {Tools: []*mcpsdk.Tool{{Name: "KM_文档解析"}}}},
		callFn: func(p *mcpsdk.CallToolParams) (*mcpsdk.CallToolResult, error) {
			calledWith = p.Name
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "ok"}}}, nil
		},
	}
	tools, _ := newClientWith(fs).Tools(context.Background())
	if tools[0].Name() == "KM_文档解析" {
		t.Fatal("sanitized name should not contain Chinese characters")
	}
	if _, err := tools[0].Execute(context.Background(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if calledWith != "KM_文档解析" {
		t.Errorf("CallTool received %q, want original remote name", calledWith)
	}
}

func TestClose(t *testing.T) {
	fs := &fakeSession{}
	if err := newClientWith(fs).Close(); err != nil || !fs.closed {
		t.Errorf("Close not propagated: err=%v closed=%v", err, fs.closed)
	}
}

// ---- integration test against the real SDK over in-memory transport ----

type addArgs struct {
	A int `json:"a" jsonschema:"first addend"`
	B int `json:"b" jsonschema:"second addend"`
}

func TestIntegration_inMemoryServer(t *testing.T) {
	ctx := context.Background()
	clientT, serverT := mcpsdk.NewInMemoryTransports()

	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test-server", Version: "1.0.0"}, nil)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "add", Description: "add two ints"},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in addArgs) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{
				Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: strconv.Itoa(in.A + in.B)}},
			}, nil, nil
		})
	ss, err := srv.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()

	cli := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "goforge-test", Version: "0.1.0"}, nil)
	cs, err := cli.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	c := newClientWith(cs)
	defer c.Close()

	tools, err := c.Tools(ctx)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	var addTool tool.Tool
	for _, tl := range tools {
		if tl.Name() == "add" {
			addTool = tl
		}
	}
	if addTool == nil {
		t.Fatalf("add tool not discovered; got %d tools", len(tools))
	}
	// The discovered schema should carry the inferred parameters.
	raw, _ := json.Marshal(addTool.Schema().Parameters)
	if !strings.Contains(string(raw), `"a"`) || !strings.Contains(string(raw), `"b"`) {
		t.Errorf("inferred schema missing params: %s", raw)
	}

	out, err := addTool.Execute(ctx, json.RawMessage(`{"a":2,"b":3}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "5" {
		t.Errorf("add(2,3) = %q, want 5", out)
	}
}
