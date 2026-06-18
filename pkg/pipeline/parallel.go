package pipeline

import (
	"context"
	"fmt"

	"golang.org/x/sync/errgroup"
)

// ParallelStage composes sub-stages into a SINGLE node that runs them
// concurrently on the same input and merges their outputs with join. It is the
// fan-out/fan-in primitive (M6-006 orchestrator-worker) — and it is transparent
// to the FSM: the engine still sees one current stage, so checkpoint/resume/audit
// stay linear and a graph runtime is never needed.
//
// Sub-stages run via errgroup (one failure cancels the rest and fails the
// composite). They share the blackboard (deps.State); to avoid clobbering each
// other, parallel sub-stages should write through reducer-guarded keys (e.g.
// AppendReducer) rather than plain Set. Sub-stage gates are not evaluated — gating
// is the composite's responsibility (set Gate on the returned Stage if needed).
func ParallelStage[In, Out any](name string, subs []Stage[In, Out], join func([]Out) Out) Stage[In, Out] {
	return Stage[In, Out]{
		Name: name,
		Run: func(ctx context.Context, in In, deps StageDeps) (Out, error) {
			outs := make([]Out, len(subs))
			g, gctx := errgroup.WithContext(ctx)
			for i, s := range subs {
				g.Go(func() error {
					n, err := s.compile()
					if err != nil {
						return err
					}
					raw, err := n.run(gctx, in, deps)
					if err != nil {
						return fmt.Errorf("parallel sub-stage %q: %w", s.Name, err)
					}
					o, ok := raw.(Out)
					if !ok {
						return fmt.Errorf("parallel sub-stage %q: output type %T is not %T", s.Name, raw, *new(Out))
					}
					outs[i] = o
					return nil
				})
			}
			if err := g.Wait(); err != nil {
				var zero Out
				return zero, err
			}
			return join(outs), nil
		},
	}
}
