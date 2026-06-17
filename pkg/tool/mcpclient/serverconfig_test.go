package mcpclient

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

func TestServerSpec_ToConfig(t *testing.T) {
	tests := []struct {
		name    string
		spec    ServerSpec
		want    TransportKind
		wantErr bool
	}{
		{"stdio", ServerSpec{Command: "npx", Args: []string{"-y", "srv"}, Env: map[string]string{"K": "V"}}, Stdio, false},
		{"remote default streamable", ServerSpec{URL: "https://x/mcp"}, StreamableHTTP, false},
		{"remote http alias", ServerSpec{URL: "https://x/mcp", Type: "http"}, StreamableHTTP, false},
		{"remote sse", ServerSpec{URL: "https://x/mcp", Type: "sse"}, SSE, false},
		{"remote unknown type", ServerSpec{URL: "https://x/mcp", Type: "grpc"}, 0, true},
		{"neither url nor command", ServerSpec{}, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := tt.spec.ToConfig("srv")
			if tt.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if cfg.Kind != tt.want {
				t.Errorf("Kind = %d, want %d", cfg.Kind, tt.want)
			}
			if tt.spec.Command != "" && (cfg.Command != tt.spec.Command || cfg.Env["K"] != tt.spec.Env["K"]) {
				t.Errorf("stdio fields not mapped: %+v", cfg)
			}
		})
	}
}

func TestLoadServersConfig(t *testing.T) {
	t.Run("missing file is empty", func(t *testing.T) {
		sc, err := LoadServersConfig(filepath.Join(t.TempDir(), "none.json"))
		if err != nil || len(sc.MCPServers) != 0 {
			t.Errorf("missing file: sc=%+v err=%v", sc, err)
		}
	})

	t.Run("parse standard file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ".mcp.json")
		content := `{
		  "mcpServers": {
		    "fs":     {"command":"npx","args":["-y","server-fs","/p"],"env":{"TOKEN":"x"}},
		    "remote": {"url":"https://example.com/mcp","type":"sse","headers":{"Authorization":"Bearer t"}}
		  }
		}`
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		sc, err := LoadServersConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(sc.MCPServers) != 2 {
			t.Fatalf("got %d servers, want 2", len(sc.MCPServers))
		}
		fs, _ := sc.MCPServers["fs"].ToConfig("fs")
		if fs.Kind != Stdio || fs.Command != "npx" || fs.Env["TOKEN"] != "x" {
			t.Errorf("fs spec wrong: %+v", fs)
		}
		remote, _ := sc.MCPServers["remote"].ToConfig("remote")
		if remote.Kind != SSE || remote.Headers["Authorization"] != "Bearer t" {
			t.Errorf("remote spec wrong: %+v", remote)
		}
	})
}

func TestHTTPClient_injectsHeaders(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
	}))
	defer srv.Close()

	c := httpClient(map[string]string{"Authorization": "Bearer abc"}, nil)
	if c == nil {
		t.Fatal("expected non-nil client when headers given")
	}
	if _, err := c.Get(srv.URL); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer abc" {
		t.Errorf("header not injected: %q", got)
	}

	if httpClient(nil, nil) != nil {
		t.Error("expected nil client when no headers and no token source")
	}
}

func TestHTTPClient_oauthTokenInjected(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
	}))
	defer srv.Close()

	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "tok123", TokenType: "Bearer"})
	c := httpClient(nil, ts)
	if c == nil {
		t.Fatal("expected non-nil client when token source given")
	}
	if _, err := c.Get(srv.URL); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer tok123" {
		t.Errorf("oauth token not injected: %q", got)
	}
}

func TestToConfig_oauth(t *testing.T) {
	spec := ServerSpec{URL: "https://x/mcp", OAuth: &OAuthSpec{ClientID: "id", ClientSecret: "sec", TokenURL: "https://x/token", Scopes: []string{"a"}}}
	cfg, err := spec.ToConfig("remote")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TokenSource == nil {
		t.Error("expected a TokenSource from oauth block")
	}

	bad := ServerSpec{URL: "https://x/mcp", OAuth: &OAuthSpec{ClientSecret: "sec"}} // no clientId/tokenUrl
	if _, err := bad.ToConfig("remote"); err == nil {
		t.Error("expected error for incomplete oauth block")
	}
}

func TestToConfig_authorizationCode(t *testing.T) {
	t.Run("builds OAuthHandler (DCR)", func(t *testing.T) {
		spec := ServerSpec{URL: "https://x/mcp", Type: "streamable", OAuth: &OAuthSpec{Flow: "authorization_code"}}
		cfg, err := spec.ToConfig("remote")
		if err != nil {
			t.Fatal(err)
		}
		if cfg.OAuthHandler == nil {
			t.Error("expected an OAuthHandler for authorization_code")
		}
		if cfg.TokenSource != nil {
			t.Error("authorization_code should not set the client-credentials TokenSource")
		}
	})

	t.Run("unknown flow errors", func(t *testing.T) {
		spec := ServerSpec{URL: "https://x/mcp", OAuth: &OAuthSpec{Flow: "device_code"}}
		if _, err := spec.ToConfig("remote"); err == nil {
			t.Error("expected error for unknown oauth flow")
		}
	})

	t.Run("authorization_code + SSE rejected at transport", func(t *testing.T) {
		spec := ServerSpec{URL: "https://x/mcp", Type: "sse", OAuth: &OAuthSpec{Flow: "authorization_code"}}
		cfg, err := spec.ToConfig("remote")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := newTransport(cfg); err == nil {
			t.Error("expected error: interactive OAuth over SSE")
		}
	})
}

func TestNewMCPTool_prefix(t *testing.T) {
	mt := &mcpsdk.Tool{Name: "read_file", Description: "d"}
	tl, err := newMCPTool(nil, mt, "filesystem")
	if err != nil {
		t.Fatal(err)
	}
	if tl.Name() != "filesystem_read_file" {
		t.Errorf("prefixed name = %q, want filesystem_read_file", tl.Name())
	}
	// No prefix ⇒ bare (sanitized) name.
	tl2, _ := newMCPTool(nil, mt, "")
	if tl2.Name() != "read_file" {
		t.Errorf("unprefixed name = %q, want read_file", tl2.Name())
	}
}

func TestNewTransport_kinds(t *testing.T) {
	stdio, err := newTransport(Config{Kind: Stdio, Command: "echo", Env: map[string]string{"K": "V"}})
	if err != nil {
		t.Fatalf("stdio: %v", err)
	}
	if _, ok := stdio.(*mcpsdk.CommandTransport); !ok {
		t.Errorf("stdio transport = %T, want *mcpsdk.CommandTransport", stdio)
	}

	streamable, err := newTransport(Config{Kind: StreamableHTTP, URL: "https://x"})
	if err != nil {
		t.Fatalf("streamable: %v", err)
	}
	if _, ok := streamable.(*mcpsdk.StreamableClientTransport); !ok {
		t.Errorf("streamable transport = %T", streamable)
	}

	sse, err := newTransport(Config{Kind: SSE, URL: "https://x"})
	if err != nil {
		t.Fatalf("sse: %v", err)
	}
	if _, ok := sse.(*mcpsdk.SSEClientTransport); !ok {
		t.Errorf("sse transport = %T", sse)
	}

	if _, err := newTransport(Config{Kind: StreamableHTTP}); err == nil {
		t.Error("expected error for http without url")
	}
	if _, err := newTransport(Config{Kind: Stdio}); err == nil {
		t.Error("expected error for stdio without command")
	}
}
