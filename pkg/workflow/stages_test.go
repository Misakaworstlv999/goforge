package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/Misakaworstlv999/goforge/pkg/pipeline"
)

func TestRequirementStage(t *testing.T) {
	specJSON := `{
		"summary": "add an Add function",
		"scope": ["math"],
		"acceptance": [
			{"id": "AP-1", "description": "Add(2,3) == 5", "kind": "unit"},
			{"id": "AP-2", "description": "package builds", "kind": "integration"}
		]
	}`
	deps := pipeline.StageDeps{LLM: &jsonLLM{reply: specJSON}, State: pipeline.NewState()}
	st := NewRequirementStage(Config{KMMCPServer: "-"})
	ctx := context.Background()

	if _, err := st.Run(ctx, "Add an Add(a,b int) int function", deps); err != nil {
		t.Fatal(err)
	}

	// Spec + acceptance contract seeded onto the blackboard.
	spec, ok := getSpec(deps.State)
	if !ok || len(spec.Acceptance) != 2 {
		t.Fatalf("spec not on blackboard: %+v", spec)
	}
	got := getAcceptance(deps.State)
	if len(got) != 2 || got[1].Kind != KindIntegration {
		t.Errorf("acceptance contract not seeded: %+v", got)
	}

	// Gate passes with ≥1 acceptance point.
	res, _ := st.Gate(ctx, "", deps)
	if res.Status != pipeline.GatePass {
		t.Errorf("gate should pass with acceptance points, got %v (%q)", res.Status, res.Reason)
	}

	// Gate fails (→ retry) when the spec has no acceptance points.
	empty := pipeline.StageDeps{LLM: &jsonLLM{reply: `{"summary":"x","acceptance":[]}`}, State: pipeline.NewState()}
	stEmpty := NewRequirementStage(Config{KMMCPServer: "-"})
	_, _ = stEmpty.Run(ctx, "x", empty)
	if r, _ := stEmpty.Gate(ctx, "", empty); r.Status != pipeline.GateFail {
		t.Error("gate should fail when no acceptance points")
	}
}

func TestTechDesignStage(t *testing.T) {
	deps := pipeline.StageDeps{
		LLM:   &jsonLLM{reply: `{"approach":"add a func","files":["math.go"],"risks":["none"]}`},
		State: pipeline.NewState(),
	}
	// Seed the spec the design stage reads from the blackboard.
	deps.State.Set(specKey, Spec{Summary: "add", Acceptance: []AcceptancePoint{{ID: "AP-1", Kind: KindUnit}}})
	st := NewTechDesignStage(Config{KMMCPServer: "-"})
	ctx := context.Background()

	if _, err := st.Run(ctx, "", deps); err != nil {
		t.Fatal(err)
	}
	design, ok := getDesign(deps.State)
	if !ok || len(design.Files) != 1 || design.Files[0] != "math.go" {
		t.Fatalf("design not on blackboard: %+v", design)
	}
	// Spec carried inside the design for downstream rendering.
	if len(design.Spec.Acceptance) != 1 || design.Spec.Acceptance[0].ID != "AP-1" {
		t.Errorf("spec not carried into design: %+v", design.Spec)
	}
	if r, _ := st.Gate(ctx, "", deps); r.Status != pipeline.GatePass {
		t.Errorf("gate should pass with files, got %v", r.Status)
	}
}

func TestCodingStage_scopedToolsAndBlackboard(t *testing.T) {
	deps := pipeline.StageDeps{
		LLM:   &jsonLLM{reply: `{"files":["math.go"],"summary":"implemented Add"}`},
		State: pipeline.NewState(),
	}
	deps.State.Set(designKey, Design{Approach: "x", Files: []string{"math.go"}})
	st := NewCodingStage(Config{})

	if got := strings.Join(st.Tools, ","); got != "read_file,write_file,list_files,exec_command" {
		t.Errorf("coding tool scope = %q", got)
	}
	if _, err := st.Run(context.Background(), "", deps); err != nil {
		t.Fatal(err)
	}
	cc, ok := getCode(deps.State)
	if !ok || cc.Summary != "implemented Add" || cc.Design.Approach != "x" {
		t.Errorf("code change not on blackboard / design not carried: %+v", cc)
	}
}

func TestReviewStage_verdictDrivesRoute(t *testing.T) {
	ctx := context.Background()
	route := reviewRoute(StageTestUnit, StageCoding)
	seed := func(d pipeline.StageDeps) {
		d.State.Set(codeKey, CodeChange{Files: []string{"math.go"}, Summary: "Add"})
	}

	t.Run("pass verdict → forward to test_unit", func(t *testing.T) {
		deps := pipeline.StageDeps{LLM: &jsonLLM{reply: `{"pass":true,"reason":"clean"}`}, State: pipeline.NewState()}
		seed(deps)
		if _, err := NewReviewStage().Run(ctx, "", deps); err != nil {
			t.Fatal(err)
		}
		if r, _ := route(ctx, "", deps.State); r.Next != StageTestUnit {
			t.Errorf("got Next=%q, want %q", r.Next, StageTestUnit)
		}
	})

	t.Run("fail verdict → back-edge to coding", func(t *testing.T) {
		deps := pipeline.StageDeps{LLM: &jsonLLM{reply: `{"pass":false,"reason":"needs work"}`}, State: pipeline.NewState()}
		seed(deps)
		if _, err := NewReviewStage().Run(ctx, "", deps); err != nil {
			t.Fatal(err)
		}
		if r, _ := route(ctx, "", deps.State); r.Next != StageCoding {
			t.Errorf("got Next=%q, want back-edge to %q", r.Next, StageCoding)
		}
	})

	// The reviewer is read-only: it must not hold write/exec tools.
	for _, tl := range NewReviewStage().Tools {
		if tl == "write_file" || tl == "exec_command" {
			t.Errorf("reviewer must not have %q", tl)
		}
	}
}

func TestAnalysisStages_kmToolFilter(t *testing.T) {
	// KM integration is opt-in: no server configured ⇒ no ToolFilter.
	if NewRequirementStage(Config{}).ToolFilter != nil {
		t.Error("empty config must not set a KM ToolFilter (opt-in)")
	}
	if NewRequirementStage(Config{KMMCPServer: "-"}).ToolFilter != nil {
		t.Error("explicit '-' must disable the KM ToolFilter")
	}
	// Configuring a server enables the bulk KM tool filter on both analysis stages.
	if NewRequirementStage(Config{KMMCPServer: "docs"}).ToolFilter == nil {
		t.Error("a configured KM server must enable the ToolFilter")
	}
	if NewTechDesignStage(Config{KMMCPServer: "docs"}).ToolFilter == nil {
		t.Error("a configured KM server must enable the ToolFilter on techdesign")
	}
}
