package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Misakaworstlv999/goforge/pkg/agent"
	"github.com/Misakaworstlv999/goforge/pkg/pipeline"
	"github.com/Misakaworstlv999/goforge/pkg/tool"
	"github.com/Misakaworstlv999/goforge/pkg/tool/mcpclient"
)

// codingTools is the tool subset the coding/testing agents are allowed (per-stage
// scoping). The pipeline's shared registry must provide these (built from a
// Sandbox by the caller / BuildDevWorkflow).
var codingTools = []string{"read_file", "write_file", "list_files", "exec_command"}

// Stage names — also the FSM node keys the workflow graph routes between.
const (
	StageRequirement = "requirement"
	StageTechDesign  = "techdesign"
	StageCoding      = "coding"
	StageReview      = "review"
	StageTestUnit    = "test_unit"
	StageTestIntegr  = "test_integration"
	StageTestE2E     = "test_e2e"
	StageAcceptance  = "acceptance"
)

// Blackboard keys. Because the workflow is a GRAPH (back-edges, chained test
// layers), the typed Stage[Out]→[In] hand-off does not line up across nodes, so
// artifacts flow through the shared blackboard (the M5 cross-stage data bus)
// while stage edges carry only a uniform status string. Typing lives in the
// typed getters/setters below.
const (
	specKey       = "spec"
	designKey     = "design"
	codeKey       = "code"
	reportKey     = "report"
	reviewKey     = pipeline.TempPrefix + "review"      // not persisted
	testPassedKey = pipeline.TempPrefix + "test_passed" // last test layer result, drives testRoute
	// acceptanceKey is defined in types.go (the contract).
)

func getArtifact[T any](st *pipeline.State, key string) (T, bool) {
	var zero T
	if v, ok := st.Get(key); ok {
		if t, ok := v.(T); ok {
			return t, true
		}
	}
	return zero, false
}

func getSpec(st *pipeline.State) (Spec, bool)       { return getArtifact[Spec](st, specKey) }
func getDesign(st *pipeline.State) (Design, bool)   { return getArtifact[Design](st, designKey) }
func getCode(st *pipeline.State) (CodeChange, bool) { return getArtifact[CodeChange](st, codeKey) }

// getAcceptance reads the acceptance-point contract from the shared blackboard.
func getAcceptance(st *pipeline.State) []AcceptancePoint {
	pts, _ := getArtifact[[]AcceptancePoint](st, acceptanceKey)
	return pts
}

func getReview(st *pipeline.State) (reviewVerdict, bool) {
	return getArtifact[reviewVerdict](st, reviewKey)
}

// analysisToolFilter scopes requirement/techdesign agents to a knowledge-base
// MCP server's tools plus read-only local inspection. KM integration is opt-in:
// nil (no scoping) when no server is configured ("" or "-").
func analysisToolFilter(cfg Config) tool.Filter {
	if cfg.KMMCPServer == "" || cfg.KMMCPServer == "-" {
		return nil
	}
	return tool.Any(
		mcpclient.ServerToolFilter(cfg.KMMCPServer),
		tool.Names("read_file", "list_files"),
	)
}

// NewRequirementStage analyzes a free-text request (the pipeline's entry input)
// into a Spec whose acceptance points are the up-front contract, seeded onto the
// blackboard with the merging reducer. Its gate rejects (→ retry) a spec with no
// acceptance points.
func NewRequirementStage(cfg Config) pipeline.Stage[string, string] {
	policy := agent.ContextPolicy{Breadth: agent.BreadthWide, Depth: agent.DepthShallow}
	sys := requirementSystemPrompt(cfg.KMMCPServer)
	return pipeline.Stage[string, string]{
		Name:       StageRequirement,
		ToolFilter: analysisToolFilter(cfg),
		Policy:     policy,
		Run: func(ctx context.Context, req string, d pipeline.StageDeps) (string, error) {
			out, err := pipeline.RunAgent(ctx, d, "Feature request:\n"+req, sys, policy)
			if err != nil {
				return "", err
			}
			spec, err := decodeJSON[Spec](out)
			if err != nil {
				return "", fmt.Errorf("requirement stage: %w", err)
			}
			d.State.SetReducer(acceptanceKey, acceptanceReducer)
			d.State.Set(acceptanceKey, spec.Acceptance)
			d.State.Set(specKey, spec)
			return "spec: " + spec.Summary, nil
		},
		Gate: blackboardGate(func(st *pipeline.State) error {
			if len(getAcceptance(st)) == 0 {
				return fmt.Errorf("no acceptance points defined")
			}
			return nil
		}),
	}
}

// NewTechDesignStage turns the blackboard Spec into a Design (also stored on the
// blackboard), carrying the Spec inside it for downstream rendering.
func NewTechDesignStage(cfg Config) pipeline.Stage[string, string] {
	policy := agent.ContextPolicy{Breadth: agent.BreadthMedium, Depth: agent.DepthMedium}
	sys := techDesignSystemPrompt(cfg.KMMCPServer)
	return pipeline.Stage[string, string]{
		Name:       StageTechDesign,
		ToolFilter: analysisToolFilter(cfg),
		Policy:     policy,
		Run: func(ctx context.Context, _ string, d pipeline.StageDeps) (string, error) {
			spec, _ := getSpec(d.State)
			out, err := pipeline.RunAgent(ctx, d, "Requirement spec:\n"+renderSpec(spec), sys, policy)
			if err != nil {
				return "", err
			}
			design, err := decodeJSON[Design](out)
			if err != nil {
				return "", fmt.Errorf("techdesign stage: %w", err)
			}
			design.Spec = spec
			d.State.Set(designKey, design)
			return "design: " + strings.Join(design.Files, ", "), nil
		},
		Gate: blackboardGate(func(st *pipeline.State) error {
			if d, ok := getDesign(st); !ok || len(d.Files) == 0 {
				return fmt.Errorf("design named no files")
			}
			return nil
		}),
	}
}

// NewCodingStage implements the blackboard Design into real files using the
// sandboxed file/shell tools (scoped via Stage.Tools). It is the rework hub:
// review and every test layer route back here on failure.
func NewCodingStage(_ Config) pipeline.Stage[string, string] {
	policy := agent.ContextPolicy{Breadth: agent.BreadthNarrow, Depth: agent.DepthDeep}
	return pipeline.Stage[string, string]{
		Name:   StageCoding,
		Tools:  codingTools,
		Policy: policy,
		Run: func(ctx context.Context, _ string, d pipeline.StageDeps) (string, error) {
			design, _ := getDesign(d.State)
			out, err := pipeline.RunAgent(ctx, d, "Implement this design, then report.\n"+renderDesign(design), codingSystemPrompt(), policy)
			if err != nil {
				return "", err
			}
			cc, err := decodeJSON[CodeChange](out)
			if err != nil {
				return "", fmt.Errorf("coding stage: %w", err)
			}
			cc.Design = design
			d.State.Set(codeKey, cc)
			return "code: " + cc.Summary, nil
		},
	}
}

// reviewVerdict is the review agent's structured judgment, stashed on the
// blackboard (an invocation-scoped temp: key) for reviewRoute to read.
type reviewVerdict struct {
	Pass   bool   `json:"pass"`
	Reason string `json:"reason"`
}

const reviewSystem = `You are a strict code reviewer. Inspect the implemented files (use read_file / list_files)
and judge whether they correctly and cleanly implement the design. Reply with ONLY a JSON object:
{"pass": true, "reason": "<short justification>"}  (set pass to false if changes are needed)`

// NewReviewStage is the evaluator half of an evaluator-optimizer loop: a
// READ-ONLY reviewer agent (note the narrow tool scope) judges the code and
// stashes a verdict; reviewRoute turns a failing verdict into a back-edge to
// coding (in BuildDevWorkflow).
func NewReviewStage() pipeline.Stage[string, string] {
	policy := agent.ContextPolicy{Breadth: agent.BreadthMedium, Depth: agent.DepthMedium}
	return pipeline.Stage[string, string]{
		Name:   StageReview,
		Tools:  []string{"read_file", "list_files"}, // reviewer cannot write or exec
		Policy: policy,
		Run: func(ctx context.Context, _ string, d pipeline.StageDeps) (string, error) {
			cc, _ := getCode(d.State)
			task := "Review these files: " + strings.Join(cc.Files, ", ") + "\nSummary: " + cc.Summary
			out, err := pipeline.RunAgent(ctx, d, task, reviewSystem, policy)
			if err != nil {
				return "", err
			}
			v, err := decodeJSON[reviewVerdict](out)
			if err != nil {
				return "", fmt.Errorf("review stage: %w", err)
			}
			d.State.Set(reviewKey, v)
			return fmt.Sprintf("review: pass=%v", v.Pass), nil
		},
	}
}

// TestCommand is the shell command a test stage runs to verify a layer (e.g.
// {"go", []string{"test","./...","-count=1"}}). The stage executes it via the
// sandboxed exec_command tool and treats a non-zero exit as failure.
type TestCommand struct {
	Command string
	Args    []string
}

func testStageName(kind AcceptanceKind) string {
	switch kind {
	case KindIntegration:
		return StageTestIntegr
	case KindE2E:
		return StageTestE2E
	default:
		return StageTestUnit
	}
}

const testSystem = `You are a test engineer. Write %s-level tests for the implemented code using write_file,
then ensure they are ready to run. Reply with ONLY a JSON object: {"summary":"<what you tested>"}`

// NewTestStage is one progressive test layer. The agent writes that layer's
// tests; then the STAGE ITSELF runs the real test command via exec_command and
// derives pass/fail from the actual exit status (ground truth — not the LLM's
// self-report). It patches the acceptance points of its Kind, records the layer
// result for testRoute, and (in BuildDevWorkflow) routes a failure back to coding.
func NewTestStage(kind AcceptanceKind, cmd TestCommand) pipeline.Stage[string, string] {
	policy := agent.ContextPolicy{Breadth: agent.BreadthNarrow, Depth: agent.DepthDeep}
	return pipeline.Stage[string, string]{
		Name:   testStageName(kind),
		Tools:  codingTools,
		Policy: policy,
		Run: func(ctx context.Context, _ string, d pipeline.StageDeps) (string, error) {
			cc, _ := getCode(d.State)
			sys := fmt.Sprintf(testSystem, kind)
			if _, err := pipeline.RunAgent(ctx, d, "Write "+kind.String()+" tests for: "+strings.Join(cc.Files, ", "), sys, policy); err != nil {
				return "", err
			}
			out, passed, err := runTestCommand(ctx, d.Registry, cmd)
			if err != nil {
				return "", fmt.Errorf("%s stage: %w", testStageName(kind), err)
			}
			// Patch this kind's acceptance points from the REAL result.
			var updates []AcceptancePoint
			for _, p := range getAcceptance(d.State) {
				if p.Kind != kind {
					continue
				}
				status := StatusFail
				if passed {
					status = StatusPass
				}
				updates = append(updates, AcceptancePoint{ID: p.ID, Status: status, Evidence: firstLine(out)})
			}
			if len(updates) > 0 {
				d.State.Set(acceptanceKey, updates)
			}
			d.State.Set(reportKey, TestReport{Layers: []LayerResult{{Kind: kind, Passed: passed, Output: out}}, Acceptance: getAcceptance(d.State)})
			d.State.Set(testPassedKey, passed)
			return fmt.Sprintf("%s tests: passed=%v", kind, passed), nil
		},
	}
}

// runTestCommand invokes the exec_command tool and reads pass/fail from the exit
// status the tool annotates ("[exit status N]" on non-zero — see builtin.shell).
func runTestCommand(ctx context.Context, reg *tool.Registry, cmd TestCommand) (string, bool, error) {
	if reg == nil {
		return "", false, fmt.Errorf("no tool registry available to test stage")
	}
	tl, ok := reg.Get("exec_command")
	if !ok {
		return "", false, fmt.Errorf("exec_command tool not available to test stage")
	}
	raw, _ := json.Marshal(map[string]any{"command": cmd.Command, "args": cmd.Args})
	out, err := tl.Execute(ctx, raw)
	if err != nil {
		return out, false, err
	}
	return out, !strings.Contains(out, "[exit status"), nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// NewAcceptanceStage is the terminal gate node: it summarizes acceptance and,
// via acceptanceRoute (BuildDevWorkflow), proceeds to DONE only when every
// acceptance point is Pass; otherwise it routes back to coding.
func NewAcceptanceStage() pipeline.Stage[string, string] {
	return pipeline.Stage[string, string]{
		Name: StageAcceptance,
		Run: func(_ context.Context, _ string, d pipeline.StageDeps) (string, error) {
			ok, unmet := allAcceptancePass(d.State)
			if ok {
				return "acceptance: all points pass", nil
			}
			return "acceptance: unmet " + strings.Join(unmet, ", "), nil
		},
	}
}

// allAcceptancePass reports whether every acceptance point on the blackboard is
// Pass (and there is at least one), plus the unmet ones for diagnostics.
func allAcceptancePass(st *pipeline.State) (bool, []string) {
	pts := getAcceptance(st)
	if len(pts) == 0 {
		return false, []string{"no acceptance points"}
	}
	var unmet []string
	for _, p := range pts {
		if p.Status != StatusPass {
			unmet = append(unmet, fmt.Sprintf("%s(%s)", p.ID, p.Status))
		}
	}
	return len(unmet) == 0, unmet
}

// blackboardGate adapts a blackboard predicate into a Gate: a non-nil error is a
// gate failure (→ retry the stage). Used where a stage validates its OWN output.
func blackboardGate(check func(*pipeline.State) error) pipeline.Gate {
	return func(_ context.Context, _ any, d pipeline.StageDeps) (pipeline.GateResult, error) {
		if err := check(d.State); err != nil {
			return pipeline.GateResult{Status: pipeline.GateFail, Reason: err.Error()}, nil
		}
		return pipeline.GateResult{Status: pipeline.GatePass}, nil
	}
}

// renderSpec / renderDesign flatten artifacts into prompt text for the next agent.
func renderSpec(s Spec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Summary: %s\nScope: %s\nAcceptance points:\n", s.Summary, strings.Join(s.Scope, ", "))
	for _, ap := range s.Acceptance {
		fmt.Fprintf(&b, "  - [%s] (%s) %s\n", ap.ID, ap.Kind, ap.Description)
	}
	return b.String()
}

func renderDesign(d Design) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Approach: %s\nFiles: %s\n", d.Approach, strings.Join(d.Files, ", "))
	if len(d.Risks) > 0 {
		fmt.Fprintf(&b, "Risks: %s\n", strings.Join(d.Risks, "; "))
	}
	b.WriteString("\n" + renderSpec(d.Spec))
	return b.String()
}
