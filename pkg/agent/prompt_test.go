package agent

import (
	"strings"
	"testing"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
)

func TestBuildSystemPrompt(t *testing.T) {
	tools := []llm.ToolSchema{
		{Name: "calculator", Description: "does math"},
		{Name: "current_time", Description: "tells time"},
	}

	t.Run("role and tools listed", func(t *testing.T) {
		got := BuildSystemPrompt("You are GoForge.", tools, ContextPolicy{})
		if !strings.HasPrefix(got, "You are GoForge.") {
			t.Errorf("role not at start: %q", got)
		}
		if !strings.Contains(got, "calculator: does math") || !strings.Contains(got, "current_time: tells time") {
			t.Errorf("tools not listed: %q", got)
		}
	})

	t.Run("empty role uses default", func(t *testing.T) {
		got := BuildSystemPrompt("  ", nil, ContextPolicy{})
		if !strings.HasPrefix(got, defaultRole) {
			t.Errorf("default role not used: %q", got)
		}
	})

	t.Run("no tools omits section", func(t *testing.T) {
		got := BuildSystemPrompt("r", nil, ContextPolicy{})
		if strings.Contains(got, "Available tools") {
			t.Errorf("should not list tools section when none: %q", got)
		}
	})

	t.Run("zero policy adds no strategy hint", func(t *testing.T) {
		got := BuildSystemPrompt("r", nil, ContextPolicy{}) // wide+shallow
		if strings.Contains(got, "Context strategy") {
			t.Errorf("zero policy should add no hint: %q", got)
		}
	})

	t.Run("narrow+deep adds hint", func(t *testing.T) {
		got := BuildSystemPrompt("r", nil, ContextPolicy{Breadth: BreadthNarrow, Depth: DepthDeep})
		if !strings.Contains(got, "Context strategy") || !strings.Contains(got, "narrow") || !strings.Contains(got, "deeply") {
			t.Errorf("expected narrow+deep hint: %q", got)
		}
	})

	t.Run("deterministic and tool order stable", func(t *testing.T) {
		// Reversed input order must yield identical output (sorted internally).
		rev := []llm.ToolSchema{tools[1], tools[0]}
		a := BuildSystemPrompt("r", tools, ContextPolicy{})
		b := BuildSystemPrompt("r", rev, ContextPolicy{})
		if a != b {
			t.Errorf("tool ordering not stable:\n%q\nvs\n%q", a, b)
		}
	})
}
