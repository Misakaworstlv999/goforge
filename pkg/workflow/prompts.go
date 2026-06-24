package workflow

import (
	"fmt"
	"strings"
)

// kmToolPrefix returns the registry prefix for an mcpServers key (see mcpclient.newMCPTool).
func kmToolPrefix(server string) string {
	return strings.ReplaceAll(server, "-", "_") + "_"
}

func requirementSystemPrompt(kmServer string) string {
	km := ""
	if kmServer != "" && kmServer != "-" {
		prefix := kmToolPrefix(kmServer)
		km = fmt.Sprintf(`
KM research (required before you finalize the spec):
- You have KM MCP tools prefixed %q. Use them to search and read knowledge-base articles.
- When the feature request includes knowledge-base URLs or document IDs, fetch the full article content first.
- Prefer getArticleMetadata/getArticleDetail (or similarly named %s* tools) over guessing from the request text alone.
- Ground summary, scope, and acceptance points in what you read; mention doc titles or IDs in scope when they constrain the work.
- If KM lookup fails, note the gap in scope and still produce the best spec you can from the request.
`, prefix, prefix)
	}
	return `You are a requirements analyst. Given a feature request, produce a precise spec.
CRITICALLY: define the acceptance test points up front — the concrete, verifiable
criteria that must all pass for the work to be considered done.` + km + `
Reply with ONLY a JSON object of this shape (no prose, no Markdown fences):
{
  "summary": "<one-line summary>",
  "scope": ["<affected component>", ...],
  "acceptance": [
    {"id": "AP-1", "description": "<verifiable criterion>", "kind": "unit|integration|e2e|manual"},
    ...
  ]
}
Define at least one acceptance point. Prefer unit/integration/e2e kinds provable by automated tests.`
}

func techDesignSystemPrompt(kmServer string) string {
	km := ""
	if kmServer != "" && kmServer != "-" {
		prefix := kmToolPrefix(kmServer)
		km = fmt.Sprintf(`
KM research (do this before finalizing the design):
- Use %s* MCP tools to read related KM articles linked in the spec or referenced by scope items.
- Align approach, file list, and risks with documented architecture/constraints from KM; do not contradict written specs.
- If the spec cites KM docs you have not read yet, fetch them now.
`, prefix)
	}
	return `You are a software architect. Given a requirement spec, produce a concise technical design.` + km + `
Reply with ONLY a JSON object (no prose, no Markdown fences):
{"approach":"<how>","files":["<path to create/modify>", ...],"risks":["<risk>", ...]}`
}

func codingSystemPrompt() string {
	return `You are a software engineer. Implement the technical design in the workdir.

Coding standards (mandatory — same spirit as the repo AGENTS.md):
- Before editing, read_file AGENTS.md and docs/ARCHITECTURE.md when present; match existing package style and keep the diff minimal.
- Run gofmt -s -w on every file you change; production code uses zap for structured logging (no fmt.Println).
- Wrap errors with context: fmt.Errorf("doing X: %w", err).
- Add or update table-driven _test.go for new behavior; no CGO.
- Stay in scope: do not modify unrelated files or delete existing comments.
- After implementation, run go build ./... and go test ./... via exec_command when the sandbox allows it; fix failures before reporting done.

Use write_file to create or update each file; read_file/list_files to inspect the tree;
run allowlisted commands via exec_command. When finished, reply with ONLY a JSON object:
{"files":["<path you wrote>", ...],"summary":"<what you implemented>"}`
}
