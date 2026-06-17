// Package mcpclient is the MCP client side: it connects to external MCP servers
// and adapts their tools into GoForge's tool.Tool, exposed as a tool.ToolSet.
// The official MCP SDK dependency is isolated to this package so pkg/tool stays
// SDK-free.
package mcpclient

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

// TransportKind selects how to reach the MCP server.
type TransportKind int

const (
	// Stdio launches the server as a subprocess and speaks over its stdio.
	Stdio TransportKind = iota
	// StreamableHTTP connects to a streamable-HTTP MCP endpoint (modern remote).
	StreamableHTTP
	// SSE connects to a server-sent-events MCP endpoint (2024-11-05 transport).
	SSE
)

// Config describes one MCP server connection.
type Config struct {
	Name    string // client identity sent to the server
	Version string
	Kind    TransportKind
	Command string            // Stdio: program to run
	Args    []string          // Stdio: program arguments
	Env     map[string]string // Stdio: extra environment (merged over os.Environ)
	URL     string            // StreamableHTTP/SSE: endpoint
	Headers map[string]string // StreamableHTTP/SSE: extra HTTP headers (e.g. auth)
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
		cmd := exec.Command(cfg.Command, cfg.Args...)
		if len(cfg.Env) > 0 {
			cmd.Env = os.Environ()
			for k, v := range cfg.Env {
				cmd.Env = append(cmd.Env, k+"="+v)
			}
		}
		return &mcpsdk.CommandTransport{Command: cmd}, nil
	case StreamableHTTP:
		if cfg.URL == "" {
			return nil, fmt.Errorf("mcp streamable-http transport requires a url")
		}
		return &mcpsdk.StreamableClientTransport{Endpoint: cfg.URL, HTTPClient: httpClient(cfg.Headers)}, nil
	case SSE:
		if cfg.URL == "" {
			return nil, fmt.Errorf("mcp sse transport requires a url")
		}
		return &mcpsdk.SSEClientTransport{Endpoint: cfg.URL, HTTPClient: httpClient(cfg.Headers)}, nil
	default:
		return nil, fmt.Errorf("unknown mcp transport kind %d", cfg.Kind)
	}
}

// httpClient returns an *http.Client that injects the given headers on every
// request, or nil when there are none (the SDK then uses http.DefaultClient).
func httpClient(headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return nil
	}
	return &http.Client{Transport: headerRoundTripper{headers: headers, base: http.DefaultTransport}}
}

type headerRoundTripper struct {
	headers map[string]string
	base    http.RoundTripper
}

func (h headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone to avoid mutating a shared request.
	r := req.Clone(req.Context())
	for k, v := range h.headers {
		r.Header.Set(k, v)
	}
	return h.base.RoundTrip(r)
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
