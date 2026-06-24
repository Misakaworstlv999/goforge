package workflow

import (
	"context"

	"github.com/Misakaworstlv999/goforge/pkg/pipeline"
)

// Config parameterizes the dev workflow. The three test commands are run by the
// matching progressive test layer (via the sandboxed exec_command tool); zero
// values default to `go test ./... -count=1`. MaxSteps bounds the total stage
// executions across rework cycles (default 60).
//
// KMMCPServer names the mcpServers key for a knowledge-base MCP server whose
// tools are scoped onto the requirement/techdesign stages (so those agents can
// research docs before speccing). It is OPT-IN: empty (or "-") disables KM tool
// scoping. When set, the shared Registry must register that server's tools (see
// internal/cli registerMCPServers); the value is supplied by the deployment's
// own config, never hardcoded here.
type Config struct {
	UnitTest        TestCommand
	IntegrationTest TestCommand
	E2ETest         TestCommand
	MaxSteps        int
	KMMCPServer     string
}

func (c Config) withDefaults() Config {
	def := TestCommand{Command: "go", Args: []string{"test", "./...", "-count=1"}}
	if c.UnitTest.Command == "" {
		c.UnitTest = def
	}
	if c.IntegrationTest.Command == "" {
		c.IntegrationTest = def
	}
	if c.E2ETest.Command == "" {
		c.E2ETest = def
	}
	if c.MaxSteps <= 0 {
		c.MaxSteps = 60
	}
	// KMMCPServer is intentionally not defaulted: KM integration is opt-in and
	// its server name comes from the deployment's config, not committed code.
	return c
}

// BuildDevWorkflow assembles the dev-workflow graph on the M5 pipeline engine:
//
//	requirement → techdesign → coding → review → test_unit → test_integration
//	            → test_e2e → acceptance → DONE
//
// with coding as the rework hub: review, every test layer, and acceptance all
// route BACK to coding on failure (cyclic graph). requirement/techdesign use
// gate-fail-retry on their own malformed output; the back-edges are RouteFuncs.
// deps.Registry must provide the sandboxed file/shell tools (codingTools).
func BuildDevWorkflow(deps pipeline.StageDeps, cfg Config, opts ...pipeline.Option) *pipeline.Pipeline {
	cfg = cfg.withDefaults()
	p := pipeline.New(deps, append([]pipeline.Option{pipeline.WithMaxSteps(cfg.MaxSteps)}, opts...)...)

	// Nodes (registration order defines the default forward path).
	_ = pipeline.AddStage(p, NewRequirementStage(cfg))
	_ = pipeline.AddStage(p, NewTechDesignStage(cfg))
	_ = pipeline.AddStage(p, NewCodingStage(cfg))
	_ = pipeline.AddStage(p, NewReviewStage())
	_ = pipeline.AddStage(p, NewTestStage(KindUnit, cfg.UnitTest))
	_ = pipeline.AddStage(p, NewTestStage(KindIntegration, cfg.IntegrationTest))
	_ = pipeline.AddStage(p, NewTestStage(KindE2E, cfg.E2ETest))
	_ = pipeline.AddStage(p, NewAcceptanceStage())

	// Conditional routing / back-edges. (requirement→techdesign→coding→review
	// are the default sequential path and need no explicit route.)
	p.Route(StageReview, reviewRoute(StageTestUnit, StageCoding))
	p.Route(StageTestUnit, testRoute(StageTestIntegr, StageCoding))
	p.Route(StageTestIntegr, testRoute(StageTestE2E, StageCoding))
	p.Route(StageTestE2E, testRoute(StageAcceptance, StageCoding))
	p.Route(StageAcceptance, acceptanceRoute(StageCoding))
	return p
}

// reviewRoute proceeds to passNext when the review verdict passed, else routes
// back to failNext (coding) for rework.
func reviewRoute(passNext, failNext string) pipeline.RouteFunc {
	return func(_ context.Context, _ any, st *pipeline.State) (pipeline.Route, error) {
		if v, ok := getReview(st); ok && v.Pass {
			return pipeline.Route{Next: passNext}, nil
		}
		return pipeline.Route{Next: failNext}, nil
	}
}

// testRoute proceeds to passNext when this layer passed, else back to failNext.
// The layer result is read from the blackboard (the stage edge carries only a
// status string in the graph).
func testRoute(passNext, failNext string) pipeline.RouteFunc {
	return func(_ context.Context, _ any, st *pipeline.State) (pipeline.Route, error) {
		if passed, ok := getArtifact[bool](st, testPassedKey); ok && passed {
			return pipeline.Route{Next: passNext}, nil
		}
		return pipeline.Route{Next: failNext}, nil
	}
}

// acceptanceRoute reaches DONE only when every acceptance point is Pass; any
// unmet point routes back to coding.
func acceptanceRoute(failNext string) pipeline.RouteFunc {
	return func(_ context.Context, _ any, st *pipeline.State) (pipeline.Route, error) {
		if ok, _ := allAcceptancePass(st); ok {
			return pipeline.Route{Done: true}, nil
		}
		return pipeline.Route{Next: failNext}, nil
	}
}
