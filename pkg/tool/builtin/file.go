package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

// ReadFileArgs is the input for the read_file tool.
type ReadFileArgs struct {
	Path string `json:"path" jsonschema:"description=Path to the file to read (relative to the workdir or absolute within the sandbox),required"`
}

// NewReadFile returns a tool that reads a file's contents, confined to the sandbox.
func NewReadFile(sb *Sandbox) tool.Tool {
	return tool.NewTool("read_file", "Read the contents of a file within the allowed directories.",
		func(_ context.Context, args ReadFileArgs) (string, error) {
			abs, err := sb.resolvePath(args.Path)
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				return "", fmt.Errorf("reading %s: %w", args.Path, err)
			}
			return capOutput(string(data), sb.maxOutput), nil
		})
}

// WriteFileArgs is the input for the write_file tool.
type WriteFileArgs struct {
	Path    string `json:"path" jsonschema:"description=Path to the file to write (relative to the workdir or absolute within the sandbox),required"`
	Content string `json:"content" jsonschema:"description=The full content to write to the file,required"`
}

// NewWriteFile returns a tool that writes a file, creating parent directories
// within the sandbox as needed.
func NewWriteFile(sb *Sandbox) tool.Tool {
	return tool.NewTool("write_file", "Write content to a file within the allowed directories, creating it if needed.",
		func(_ context.Context, args WriteFileArgs) (string, error) {
			abs, err := sb.resolvePath(args.Path)
			if err != nil {
				return "", err
			}
			dir := filepath.Dir(abs)
			if _, err := sb.resolvePath(dir); err != nil {
				return "", err
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", fmt.Errorf("creating directory for %s: %w", args.Path, err)
			}
			if err := os.WriteFile(abs, []byte(args.Content), 0o644); err != nil {
				return "", fmt.Errorf("writing %s: %w", args.Path, err)
			}
			return fmt.Sprintf("wrote %d bytes to %s", len(args.Content), args.Path), nil
		})
}

// ListFilesArgs is the input for the list_files tool.
type ListFilesArgs struct {
	Path string `json:"path,omitempty" jsonschema:"description=Directory to list (relative to the workdir or absolute within the sandbox). Defaults to the workdir."`
}

// NewListFiles returns a tool that lists directory entries within the sandbox.
func NewListFiles(sb *Sandbox) tool.Tool {
	return tool.NewTool("list_files", "List the entries of a directory within the allowed directories.",
		func(_ context.Context, args ListFilesArgs) (string, error) {
			path := args.Path
			if path == "" {
				path = "."
			}
			abs, err := sb.resolvePath(path)
			if err != nil {
				return "", err
			}
			entries, err := os.ReadDir(abs)
			if err != nil {
				return "", fmt.Errorf("listing %s: %w", path, err)
			}
			if len(entries) == 0 {
				return "(empty directory)", nil
			}

			lines := make([]string, 0, len(entries))
			for _, e := range entries {
				kind := "file"
				var size int64
				if e.IsDir() {
					kind = "dir"
				} else if info, err := e.Info(); err == nil {
					size = info.Size()
				}
				if kind == "dir" {
					lines = append(lines, fmt.Sprintf("%s (dir)", e.Name()))
				} else {
					lines = append(lines, fmt.Sprintf("%s (file, %d bytes)", e.Name(), size))
				}
			}
			sort.Strings(lines)
			return strings.Join(lines, "\n"), nil
		})
}

// capOutput truncates s to at most max bytes, appending a notice when truncated.
func capOutput(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n... [truncated, %d bytes total]", len(s))
}
