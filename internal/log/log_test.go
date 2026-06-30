package log

import (
	"bytes"
	"strings"
	"testing"
)

func TestNew_levelFilteringAndFields(t *testing.T) {
	var buf bytes.Buffer
	l := New("warn", "json", &buf)

	l.Debug("d", "k", "v") // below warn ⇒ dropped
	l.Info("i", "k", "v")  // below warn ⇒ dropped
	l.Warn("a warning", "run_id", "r1")
	l.Error("an error", "code", 500)

	out := buf.String()
	if strings.Contains(out, `"d"`) || strings.Contains(out, `"i"`) {
		t.Errorf("debug/info should be filtered at warn level:\n%s", out)
	}
	if !strings.Contains(out, "a warning") || !strings.Contains(out, "run_id") || !strings.Contains(out, "r1") {
		t.Errorf("warn line missing message/fields:\n%s", out)
	}
	if !strings.Contains(out, "an error") || !strings.Contains(out, "500") {
		t.Errorf("error line missing message/fields:\n%s", out)
	}
}

func TestNop_isSilent(t *testing.T) {
	// Nop must satisfy Logger and produce no output / not panic.
	var l Logger = Nop()
	l.Debug("x")
	l.Info("x", "k", "v")
	l.Warn("x")
	l.Error("x")
}
