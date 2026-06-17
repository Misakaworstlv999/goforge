package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2/clientcredentials"
)

// ServerSpec is one entry of the standard `mcpServers` config (as used by Claude
// Desktop / Cursor / .mcp.json). A `url` selects a remote server (Type picks the
// transport); otherwise it is a stdio server launched from `command`.
type ServerSpec struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Type    string            `json:"type,omitempty"` // "", "streamable"/"http", "sse"
	Headers map[string]string `json:"headers,omitempty"`
	OAuth   *OAuthSpec        `json:"oauth,omitempty"` // remote: OAuth2 client-credentials
}

// OAuthSpec configures OAuth2 for a remote server. Flow selects the grant:
//   - "client_credentials" (default): unattended; needs ClientID/ClientSecret/TokenURL.
//   - "authorization_code": interactive browser login (OAuth 2.1 + PKCE). The auth
//     server is discovered from the MCP endpoint; a client is registered dynamically
//     unless ClientID is given. Streamable-HTTP only.
//
// (A static bearer token can instead be set directly via Headers.)
type OAuthSpec struct {
	Flow         string   `json:"flow,omitempty"` // "", "client_credentials", "authorization_code"
	ClientID     string   `json:"clientId,omitempty"`
	ClientSecret string   `json:"clientSecret,omitempty"`
	TokenURL     string   `json:"tokenUrl,omitempty"`     // client_credentials
	Scopes       []string `json:"scopes,omitempty"`       // client_credentials
	RedirectPort int      `json:"redirectPort,omitempty"` // authorization_code (default 8765)
}

const defaultRedirectPort = 8765

// ServersConfig is the top-level standard config file: {"mcpServers": {...}}.
type ServersConfig struct {
	MCPServers map[string]ServerSpec `json:"mcpServers"`
}

// LoadServersConfig reads a standard mcpServers JSON file. A missing file yields
// an empty config and no error (so an absent .mcp.json simply means "no MCP
// servers"), mirroring the optional .env file.
func LoadServersConfig(path string) (ServersConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ServersConfig{}, nil
		}
		return ServersConfig{}, fmt.Errorf("reading mcp config %s: %w", path, err)
	}
	var sc ServersConfig
	if err := json.Unmarshal(data, &sc); err != nil {
		return ServersConfig{}, fmt.Errorf("parsing mcp config %s: %w", path, err)
	}
	return sc, nil
}

// ToConfig maps a ServerSpec (named name) to a connection Config. Presence of
// URL selects a remote server; Type chooses streamable HTTP (default) or SSE.
// Otherwise it is a stdio server, which requires Command.
func (s ServerSpec) ToConfig(name string) (Config, error) {
	cfg := Config{Name: "goforge", Version: "0.1.0"}

	if s.URL != "" {
		cfg.URL = s.URL
		cfg.Headers = s.Headers
		switch s.Type {
		case "", "streamable", "streamable-http", "http":
			cfg.Kind = StreamableHTTP
		case "sse":
			cfg.Kind = SSE
		default:
			return Config{}, fmt.Errorf("mcp server %q: unknown type %q (want streamable or sse)", name, s.Type)
		}
		if s.OAuth != nil {
			if err := s.OAuth.apply(name, &cfg); err != nil {
				return Config{}, err
			}
		}
		return cfg, nil
	}

	if s.Command == "" {
		return Config{}, fmt.Errorf("mcp server %q: needs either a url (remote) or a command (stdio)", name)
	}
	cfg.Kind = Stdio
	cfg.Command = s.Command
	cfg.Args = s.Args
	cfg.Env = s.Env
	return cfg, nil
}

// apply configures cfg's OAuth fields according to the selected flow.
func (o *OAuthSpec) apply(name string, cfg *Config) error {
	switch o.Flow {
	case "", "client_credentials":
		if o.ClientID == "" || o.TokenURL == "" {
			return fmt.Errorf("mcp server %q: client_credentials oauth needs clientId and tokenUrl", name)
		}
		cc := &clientcredentials.Config{
			ClientID:     o.ClientID,
			ClientSecret: o.ClientSecret,
			TokenURL:     o.TokenURL,
			Scopes:       o.Scopes,
		}
		cfg.TokenSource = cc.TokenSource(context.Background())
		return nil

	case "authorization_code":
		port := o.RedirectPort
		if port == 0 {
			port = defaultRedirectPort
		}
		redirectURL := fmt.Sprintf("http://localhost:%d/callback", port)
		var client *oauthex.ClientCredentials
		if o.ClientID != "" {
			client = &oauthex.ClientCredentials{ClientID: o.ClientID}
			if o.ClientSecret != "" {
				client.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: o.ClientSecret}
			}
		}
		h, err := newAuthCodeHandler(redirectURL, client, defaultOpen)
		if err != nil {
			return fmt.Errorf("mcp server %q: authorization_code oauth: %w", name, err)
		}
		cfg.OAuthHandler = h
		return nil

	default:
		return fmt.Errorf("mcp server %q: unknown oauth flow %q (want client_credentials or authorization_code)", name, o.Flow)
	}
}
