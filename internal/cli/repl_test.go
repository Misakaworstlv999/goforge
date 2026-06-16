package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRepl_dispatchesAndStopsOnExit(t *testing.T) {
	var got []string
	var out bytes.Buffer

	in := strings.NewReader("hello\n\n  \nworld\nexit\nignored\n")
	err := repl(in, &out, "BANNER", func(line string) {
		got = append(got, line)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"hello", "world"}
	if len(got) != len(want) {
		t.Fatalf("turn calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("turn[%d] = %q, want %q (empty lines skipped, exit stops)", i, got[i], want[i])
		}
	}

	if !strings.Contains(out.String(), "BANNER") {
		t.Error("banner not printed")
	}
}

func TestRepl_stopsOnEOF(t *testing.T) {
	var calls int
	var out bytes.Buffer

	// No "exit" — loop must end when input is exhausted.
	in := strings.NewReader("one\ntwo\n")
	if err := repl(in, &out, "", func(string) { calls++ }); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 {
		t.Errorf("turn calls = %d, want 2", calls)
	}
}
