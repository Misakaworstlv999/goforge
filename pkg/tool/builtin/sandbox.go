package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Default sandbox limits.
const (
	defaultMaxOutput  = 64 * 1024 // 64 KiB cap on file/command output
	defaultCmdTimeout = 30 * time.Second
)

// Sandbox is the shared safety boundary for the file and shell tools. It pins
// file access to a set of allowed root directories and gates shell execution to
// an explicit command allowlist. A single instance is constructed once and
// shared by every sandboxed tool.
type Sandbox struct {
	roots       []string // absolute, symlink-resolved allowed directories
	allowedCmds map[string]bool
	maxOutput   int
	cmdTimeout  time.Duration
}

// SandboxOption configures a Sandbox.
type SandboxOption func(*Sandbox)

// WithAllowedCommands sets the shell command allowlist (matched on the program
// name, i.e. argv[0]).
func WithAllowedCommands(cmds ...string) SandboxOption {
	return func(s *Sandbox) {
		for _, c := range cmds {
			c = strings.TrimSpace(c)
			if c != "" {
				s.allowedCmds[c] = true
			}
		}
	}
}

// WithMaxOutput caps the bytes returned by read_file / exec_command output.
func WithMaxOutput(n int) SandboxOption {
	return func(s *Sandbox) {
		if n > 0 {
			s.maxOutput = n
		}
	}
}

// WithCommandTimeout sets the per-command execution deadline.
func WithCommandTimeout(d time.Duration) SandboxOption {
	return func(s *Sandbox) {
		if d > 0 {
			s.cmdTimeout = d
		}
	}
}

// NewSandbox builds a Sandbox over the given root directories. Each root is made
// absolute and symlink-resolved; roots that do not exist or are not directories
// are rejected. At least one valid root is required.
func NewSandbox(roots []string, opts ...SandboxOption) (*Sandbox, error) {
	s := &Sandbox{
		allowedCmds: make(map[string]bool),
		maxOutput:   defaultMaxOutput,
		cmdTimeout:  defaultCmdTimeout,
	}

	for _, r := range roots {
		abs, err := filepath.Abs(r)
		if err != nil {
			return nil, fmt.Errorf("resolving root %q: %w", r, err)
		}
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return nil, fmt.Errorf("resolving root %q: %w", r, err)
		}
		info, err := os.Stat(real)
		if err != nil {
			return nil, fmt.Errorf("stat root %q: %w", r, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("root %q is not a directory", r)
		}
		s.roots = append(s.roots, real)
	}
	if len(s.roots) == 0 {
		return nil, fmt.Errorf("sandbox requires at least one root directory")
	}

	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// resolvePath resolves a user-supplied path and returns its cleaned absolute
// form if it lies within an allowed root, else an error. Relative paths resolve
// against the primary root (roots[0]). Symlinks are evaluated so that a link
// inside a root cannot point outside it.
func (s *Sandbox) resolvePath(p string) (string, error) {
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(s.roots[0], abs)
	}
	abs = filepath.Clean(abs)

	// Evaluate symlinks on the longest existing ancestor so that both existing
	// targets and not-yet-created write targets are checked against the real
	// (post-symlink) location.
	real := evalExistingPrefix(abs)

	for _, root := range s.roots {
		if within(root, real) {
			return abs, nil
		}
	}
	return "", fmt.Errorf("path %q is outside the sandbox", p)
}

// commandAllowed reports whether name is in the command allowlist.
func (s *Sandbox) commandAllowed(name string) bool {
	return s.allowedCmds[name]
}

// within reports whether abs is root itself or nested under it.
func within(root, abs string) bool {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// evalExistingPrefix resolves symlinks on the longest existing prefix of abs,
// then rejoins the remaining (non-existent) trailing components. This lets us
// validate a write target whose final path component does not exist yet while
// still catching a symlinked parent directory that escapes the sandbox.
func evalExistingPrefix(abs string) string {
	cur := abs
	var trailing []string
	for {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			if len(trailing) == 0 {
				return real
			}
			// Append the unresolved trailing components back on.
			parts := append([]string{real}, reverse(trailing)...)
			return filepath.Join(parts...)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			// Reached the root without finding an existing prefix.
			return abs
		}
		trailing = append(trailing, filepath.Base(cur))
		cur = parent
	}
}

// reverse returns a reversed copy of s.
func reverse(s []string) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[len(s)-1-i] = v
	}
	return out
}
