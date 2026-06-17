package mcpclient

import (
	"encoding/json"
	"fmt"
	"os"
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
}

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
