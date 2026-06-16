package builtin

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// requireCmd skips the test if the named command isn't available, keeping the
// suite robust across environments.
func requireCmd(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%q not found in PATH", name)
	}
}

func TestExecCommand_allowed(t *testing.T) {
	requireCmd(t, "echo")
	sb := mustSandbox(t, t.TempDir(), WithAllowedCommands("echo"))
	out, err := call(t, NewExecCommand(sb), `{"command":"echo","args":["hello","world"]}`)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !strings.Contains(out, "hello world") {
		t.Errorf("output = %q", out)
	}
}

func TestExecCommand_notAllowed(t *testing.T) {
	sb := mustSandbox(t, t.TempDir(), WithAllowedCommands("echo"))
	if _, err := call(t, NewExecCommand(sb), `{"command":"rm","args":["-rf","/"]}`); err == nil {
		t.Error("expected rejection of non-allowlisted command")
	}
}

func TestExecCommand_emptyAllowlistBlocksAll(t *testing.T) {
	sb := mustSandbox(t, t.TempDir())
	if _, err := call(t, NewExecCommand(sb), `{"command":"echo","args":["hi"]}`); err == nil {
		t.Error("expected rejection when allowlist is empty")
	}
}

func TestExecCommand_nonZeroExitReturnsOutput(t *testing.T) {
	requireCmd(t, "false")
	sb := mustSandbox(t, t.TempDir(), WithAllowedCommands("false"))
	out, err := call(t, NewExecCommand(sb), `{"command":"false"}`)
	if err != nil {
		t.Fatalf("non-zero exit should not be a tool error: %v", err)
	}
	if !strings.Contains(out, "exit status") {
		t.Errorf("expected exit status note, got %q", out)
	}
}

func TestExecCommand_timeout(t *testing.T) {
	requireCmd(t, "sleep")
	sb := mustSandbox(t, t.TempDir(), WithAllowedCommands("sleep"), WithCommandTimeout(50*time.Millisecond))
	start := time.Now()
	_, err := call(t, NewExecCommand(sb), `{"command":"sleep","args":["5"]}`)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("err = %v, want timeout", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("timeout did not fire promptly")
	}
}

func TestExecCommand_outputCap(t *testing.T) {
	requireCmd(t, "echo")
	sb := mustSandbox(t, t.TempDir(), WithAllowedCommands("echo"), WithMaxOutput(5))
	out, err := call(t, NewExecCommand(sb), `{"command":"echo","args":["0123456789"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "truncated") {
		t.Errorf("expected truncation, got %q", out)
	}
}
