package mcpclient

import (
	"strings"

	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

// ServerToolFilter returns a tool.Filter selecting every tool contributed by the
// named MCP server. MCP tools are registered as "<server>_<tool>" with the
// server's name normalized ('-'→'_') and sanitized (see newMCPTool), so this is
// a prefix match — letting a pipeline stage scope "all of server X's tools"
// without enumerating each name. Pass the server's mcpServers config key (e.g.
// "github" or "km-corp").
func ServerToolFilter(server string) tool.Filter {
	prefix := sanitizeToolName(strings.ReplaceAll(server, "-", "_")) + "_"
	return tool.Prefix(prefix)
}
