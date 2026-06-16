package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func mustSandbox(t *testing.T, root string, opts ...SandboxOption) *Sandbox {
	t.Helper()
	sb, err := NewSandbox([]string{root}, opts...)
	if err != nil {
		t.Fatalf("NewSandbox: %v", err)
	}
	return sb
}

func TestNewSandbox_validation(t *testing.T) {
	t.Run("no roots", func(t *testing.T) {
		if _, err := NewSandbox(nil); err == nil {
			t.Error("expected error for no roots")
		}
	})
	t.Run("nonexistent root", func(t *testing.T) {
		if _, err := NewSandbox([]string{filepath.Join(t.TempDir(), "nope")}); err == nil {
			t.Error("expected error for nonexistent root")
		}
	})
	t.Run("file as root", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "f")
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewSandbox([]string{f}); err == nil {
			t.Error("expected error for file root")
		}
	})
}

func TestResolvePath(t *testing.T) {
	root := t.TempDir()
	sb := mustSandbox(t, root)

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"relative within", "sub/file.txt", false},
		{"dot is root", ".", false},
		{"absolute within", filepath.Join(root, "a.txt"), false},
		{"parent traversal", "../escape.txt", true},
		{"deep traversal", "sub/../../escape.txt", true},
		{"absolute outside", filepath.Dir(root), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := sb.resolvePath(tt.path)
			if tt.wantErr != (err != nil) {
				t.Errorf("resolvePath(%q) err = %v, wantErr %v", tt.path, err, tt.wantErr)
			}
		})
	}
}

func TestResolvePath_symlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	root := t.TempDir()
	outside := t.TempDir() // a sibling dir not in the sandbox

	// Create a symlink INSIDE the root that points OUTSIDE it.
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	sb := mustSandbox(t, root)

	// Accessing through the symlink must be rejected (it resolves outside root).
	if _, err := sb.resolvePath("escape/secret.txt"); err == nil {
		t.Error("expected symlink escape to be rejected")
	}
}

func TestCommandAllowed(t *testing.T) {
	sb := mustSandbox(t, t.TempDir(), WithAllowedCommands("echo", "ls", "  "))
	if !sb.commandAllowed("echo") || !sb.commandAllowed("ls") {
		t.Error("echo/ls should be allowed")
	}
	if sb.commandAllowed("rm") {
		t.Error("rm should not be allowed")
	}
	if sb.commandAllowed("") {
		t.Error("blank command should not be allowed")
	}
}
