package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

func TestPipeline_perStageToolScoping(t *testing.T) {
	type empty struct{}
	mk := func(name string) tool.Tool {
		return tool.NewTool(name, "d", func(context.Context, empty) (string, error) { return "ok", nil })
	}
	reg := tool.NewRegistry()
	must(t, reg.Register(mk("read_file"), mk("write_file"), mk("search")))

	capture := func(deps StageDeps) []string {
		var names []string
		for _, s := range deps.Registry.Schemas() { // Schemas() is sorted by name
			names = append(names, s.Name)
		}
		return names
	}
	var coder, reviewer []string

	p := New(StageDeps{Registry: reg})
	must(t, AddStage(p, Stage[string, string]{
		Name:  "code",
		Tools: []string{"read_file", "write_file"},
		Run: func(_ context.Context, in string, deps StageDeps) (string, error) {
			coder = capture(deps)
			return in, nil
		},
	}))
	must(t, AddStage(p, Stage[string, string]{
		Name:  "review",
		Tools: []string{"read_file"}, // read-only reviewer
		Run: func(_ context.Context, in string, deps StageDeps) (string, error) {
			reviewer = capture(deps)
			return in, nil
		},
	}))

	if _, err := collect(p.Run(context.Background(), "p", "x")); err != nil {
		t.Fatal(err)
	}
	if strings.Join(coder, ",") != "read_file,write_file" {
		t.Errorf("coder tools = %v, want [read_file write_file]", coder)
	}
	if strings.Join(reviewer, ",") != "read_file" {
		t.Errorf("reviewer tools = %v, want [read_file]", reviewer)
	}
	// the shared registry is a superset, never mutated by filtering.
	if len(reg.Schemas()) != 3 {
		t.Errorf("shared registry mutated: %d tools, want 3", len(reg.Schemas()))
	}
}

func TestPipeline_emptyToolsUsesFullRegistry(t *testing.T) {
	type empty struct{}
	reg := tool.NewRegistry()
	must(t, reg.Register(tool.NewTool("a", "d", func(context.Context, empty) (string, error) { return "", nil })))

	var seen int
	p := New(StageDeps{Registry: reg})
	must(t, AddStage(p, Stage[string, string]{
		Name: "s", // no Tools ⇒ full registry
		Run: func(_ context.Context, in string, deps StageDeps) (string, error) {
			seen = len(deps.Registry.Schemas())
			return in, nil
		},
	}))
	if _, err := collect(p.Run(context.Background(), "p", "x")); err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Errorf("stage saw %d tools, want full registry (1)", seen)
	}
}

func TestPipeline_unknownStageTool(t *testing.T) {
	p := New(StageDeps{Registry: tool.NewRegistry()})
	must(t, AddStage(p, Stage[string, string]{
		Name:  "s",
		Tools: []string{"ghost_tool"},
		Run:   func(_ context.Context, in string, _ StageDeps) (string, error) { return in, nil },
	}))
	_, err := collect(p.Run(context.Background(), "p", "x"))
	if err == nil || !strings.Contains(err.Error(), "ghost_tool") {
		t.Fatalf("want error mentioning ghost_tool, got %v", err)
	}
}
