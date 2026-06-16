package builtin

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

// ExecArgs is the input for the exec_command tool.
type ExecArgs struct {
	Command string   `json:"command" jsonschema:"description=The program to run (must be in the allowlist). No shell is used; this is argv[0],required"`
	Args    []string `json:"args,omitempty" jsonschema:"description=Arguments passed to the program"`
}

// NewExecCommand returns a tool that runs an allowlisted command without a shell.
// The command name must be in the sandbox's allowlist; arguments are passed
// directly to the process (no shell interpretation, so metacharacters like ; | &
// are inert). Execution runs in the primary sandbox root with a per-call timeout.
func NewExecCommand(sb *Sandbox) tool.Tool {
	return tool.NewTool("exec_command", "Run an allowlisted command (no shell) within the workdir and return its output.",
		func(ctx context.Context, args ExecArgs) (string, error) {
			if args.Command == "" {
				return "", fmt.Errorf("command is required")
			}
			if !sb.commandAllowed(args.Command) {
				return "", fmt.Errorf("command %q is not allowed", args.Command)
			}

			ctx, cancel := context.WithTimeout(ctx, sb.cmdTimeout)
			defer cancel()

			cmd := exec.CommandContext(ctx, args.Command, args.Args...)
			cmd.Dir = sb.roots[0]
			out, err := cmd.CombinedOutput()
			output := capOutput(string(out), sb.maxOutput)

			if ctx.Err() == context.DeadlineExceeded {
				return "", fmt.Errorf("command %q timed out after %s", args.Command, sb.cmdTimeout)
			}
			if err != nil {
				// A non-zero exit is a normal observation for the agent: return the
				// output annotated with the exit status rather than dropping it.
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					return fmt.Sprintf("%s\n[exit status %d]", output, exitErr.ExitCode()), nil
				}
				// Failed to start (not found, permission, etc.) is a real error.
				return "", fmt.Errorf("running %q: %w", args.Command, err)
			}
			if output == "" {
				return "(no output)", nil
			}
			return output, nil
		})
}
