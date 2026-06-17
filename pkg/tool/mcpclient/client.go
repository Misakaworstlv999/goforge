// Package mcpclient is the MCP client side: it connects to external MCP servers
// and adapts their tools into GoForge's tool.Tool, exposed as a tool.ToolSet.
// The official MCP SDK dependency is isolated to this package so pkg/tool stays
// SDK-free.
package mcpclient

import (
	"context"
	"fmt"
	"os/exec"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

// TransportKind selects how to reach the MCP server.
type TransportKind int

const (
	// Stdio launches the server as a subprocess and speaks over its stdio.
	Stdio TransportKind = iota
	// HTTP connects to a streamable-HTTP MCP endpoint.
	HTTP
)

// Config describes one MCP server connection.
type Config struct {
	Name    string // client identity sent to the server
	Version string
	Kind    TransportKind
	Command string   // Stdio: program to run
	Args    []string // Stdio: program arguments
	URL     string   // HTTP: streamable endpoint
}

// session is the narrow slice of the SDK ClientSession this package needs. It
// exists so tests can inject a fake without a live server.
type session interface {
	ListTools(ctx context.Context, params *mcpsdk.ListToolsParams) (*mcpsdk.ListToolsResult, error)
	CallTool(ctx context.Context, params *mcpsdk.CallToolParams) (*mcpsdk.CallToolResult, error)
	Close() error
}

// Client is a connected MCP server, exposed as a tool.ToolSet. Close it to tear
// down the session (and the subprocess, for Stdio).
type Client struct {
	sess session
}

// compile-time: Client is a ToolSet.
var _ tool.ToolSet = (*Client)(nil)

// New connects to the MCP server described by cfg and returns a ready Client.
// Connection is eager (fail-fast).
func New(ctx context.Context, cfg Config) (*Client, error) {
	transport, err := newTransport(cfg)
	if err != nil {
		return nil, err
	}
	name := cfg.Name
	if name == "" {
		name = "goforge"
	}
	version := cfg.Version
	if version == "" {
		version = "0.1.0"
	}

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: name, Version: version}, nil)
	cs, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp connect: %w", err)
	}
	return &Client{sess: cs}, nil
}

// newClientWith builds a Client over an injected session (for tests).
func newClientWith(s session) *Client { return &Client{sess: s} }

func newTransport(cfg Config) (mcpsdk.Transport, error) {
	switch cfg.Kind {
	case Stdio:
		if cfg.Command == "" {
			return nil, fmt.Errorf("mcp stdio transport requires a command")
		}
		return &mcpsdk.CommandTransport{Command: exec.Command(cfg.Command, cfg.Args...)}, nil
	case HTTP:
		if cfg.URL == "" {
			return nil, fmt.Errorf("mcp http transport requires a url")
		}
		return &mcpsdk.StreamableClientTransport{Endpoint: cfg.URL}, nil
	default:
		return nil, fmt.Errorf("unknown mcp transport kind %d", cfg.Kind)
	}
}

// Tools discovers the server's tools (paginating via cursor) and adapts each
// into a tool.Tool. Implements tool.ToolSet.
func (c *Client) Tools(ctx context.Context) ([]tool.Tool, error) {
	var out []tool.Tool
	cursor := ""
	for {
		res, err := c.sess.ListTools(ctx, &mcpsdk.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("mcp list tools: %w", err)
		}
		for _, mt := range res.Tools {
			t, err := newMCPTool(c.sess, mt)
			if err != nil {
				return nil, err
			}
			out = append(out, t)
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return out, nil
}

// Close tears down the session (and subprocess for Stdio).
func (c *Client) Close() error { return c.sess.Close() }
