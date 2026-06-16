package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// call is a helper to invoke a tool with JSON args.
func call(t *testing.T, tl interface {
	Execute(context.Context, json.RawMessage) (string, error)
}, args string) (string, error) {
	t.Helper()
	return tl.Execute(context.Background(), json.RawMessage(args))
}

func TestWriteThenRead_roundTrip(t *testing.T) {
	root := t.TempDir()
	sb := mustSandbox(t, root)
	write := NewWriteFile(sb)
	read := NewReadFile(sb)

	out, err := call(t, write, `{"path":"notes/hello.txt","content":"hi there"}`)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(out, "wrote 8 bytes") {
		t.Errorf("write output = %q", out)
	}
	// File actually exists on disk under root.
	if _, err := os.Stat(filepath.Join(root, "notes", "hello.txt")); err != nil {
		t.Fatalf("file not created: %v", err)
	}

	got, err := call(t, read, `{"path":"notes/hello.txt"}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != "hi there" {
		t.Errorf("read = %q, want %q", got, "hi there")
	}
}

func TestReadFile_truncation(t *testing.T) {
	root := t.TempDir()
	sb := mustSandbox(t, root, WithMaxOutput(10))
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte("0123456789ABCDEF"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := call(t, NewReadFile(sb), `{"path":"big.txt"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "0123456789") || !strings.Contains(got, "truncated") {
		t.Errorf("expected truncated output, got %q", got)
	}
}

func TestFileTools_rejectOutsideSandbox(t *testing.T) {
	sb := mustSandbox(t, t.TempDir())
	cases := []struct {
		name string
		tl   interface {
			Execute(context.Context, json.RawMessage) (string, error)
		}
		args string
	}{
		{"read traversal", NewReadFile(sb), `{"path":"../../etc/passwd"}`},
		{"write traversal", NewWriteFile(sb), `{"path":"../evil.txt","content":"x"}`},
		{"list traversal", NewListFiles(sb), `{"path":"../.."}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := call(t, tc.tl, tc.args); err == nil {
				t.Error("expected sandbox rejection")
			}
		})
	}
}

func TestListFiles(t *testing.T) {
	root := t.TempDir()
	sb := mustSandbox(t, root)
	_ = os.WriteFile(filepath.Join(root, "a.txt"), []byte("aa"), 0o600)
	_ = os.Mkdir(filepath.Join(root, "sub"), 0o755)

	out, err := call(t, NewListFiles(sb), `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.txt (file, 2 bytes)") || !strings.Contains(out, "sub (dir)") {
		t.Errorf("list output = %q", out)
	}
}

func TestListFiles_empty(t *testing.T) {
	sb := mustSandbox(t, t.TempDir())
	out, err := call(t, NewListFiles(sb), `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "(empty directory)" {
		t.Errorf("got %q", out)
	}
}
