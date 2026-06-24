package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

// ControlTools exposes a Manager's run-control surface as a tool set, so an
// in-process supervisor agent can observe and steer worker runs via function
// calls — the exact same operations a human drives over HTTP/CLI. This is how
// GoForge internalizes "agent as controller" without a separate MCP/A2A server.
//
// Register the returned tools (or scope them to a supervisor stage via
// Stage.Tools / ToolFilter, e.g. tool.Prefix("run_")).
func ControlTools(m *Manager) []tool.Tool {
	type runID struct {
		RunID string `json:"run_id" jsonschema:"description=The run id,required"`
	}
	return []tool.Tool{
		tool.NewTool("trigger_run", "Start a new pipeline run with the given input; returns its run id.",
			func(_ context.Context, a struct {
				Input string `json:"input" jsonschema:"description=Initial request/input for the run,required"`
			}) (string, error) {
				id, err := m.Trigger("", a.Input)
				if err != nil {
					return "", err
				}
				return "started run " + id, nil
			}),

		tool.NewTool("list_runs", "List all known run ids.",
			func(_ context.Context, _ struct{}) (string, error) {
				ids := m.List()
				if len(ids) == 0 {
					return "(no runs)", nil
				}
				return strings.Join(ids, "\n"), nil
			}),

		tool.NewTool("get_run_state", "Get a run's current status, stage, and blackboard (JSON).",
			func(ctx context.Context, a runID) (string, error) {
				st, err := m.State(ctx, a.RunID)
				if err != nil {
					return "", err
				}
				b, _ := json.Marshal(map[string]any{
					"run_id":        st.PipelineID,
					"status":        st.Status.String(),
					"current_stage": st.CurrentStage,
					"retry_count":   st.RetryCount,
					"blackboard":    st.Blackboard,
				})
				return string(b), nil
			}),

		tool.NewTool("get_run_events", "Get a run's event log so far (one line per event).",
			func(_ context.Context, a runID) (string, error) {
				events, err := m.Events(a.RunID)
				if err != nil {
					return "", err
				}
				if len(events) == 0 {
					return "(no events yet)", nil
				}
				var b strings.Builder
				for _, e := range events {
					fmt.Fprintf(&b, "%s %s %s\n", e.Type, e.Stage, e.Detail)
				}
				return b.String(), nil
			}),

		tool.NewTool("pause_run", "Pause a run at its next stage safe point.",
			func(_ context.Context, a runID) (string, error) {
				return ack("paused", a.RunID, m.Pause(a.RunID))
			}),
		tool.NewTool("resume_run", "Resume a paused run.",
			func(_ context.Context, a runID) (string, error) {
				return ack("resumed", a.RunID, m.Resume(a.RunID))
			}),
		tool.NewTool("steer_run", "Inject guidance into a run's shared context for upcoming stages.",
			func(_ context.Context, a struct {
				RunID string `json:"run_id" jsonschema:"description=The run id,required"`
				Note  string `json:"note" jsonschema:"description=Guidance to inject,required"`
			}) (string, error) {
				return ack("steered", a.RunID, m.Steer(a.RunID, a.Note))
			}),
		tool.NewTool("redirect_run", "Route a run back to a specific stage (e.g. send review back to coding).",
			func(_ context.Context, a struct {
				RunID string `json:"run_id" jsonschema:"description=The run id,required"`
				Stage string `json:"stage" jsonschema:"description=Target stage name,required"`
				Note  string `json:"note" jsonschema:"description=Optional reason"`
			}) (string, error) {
				return ack("redirected to "+a.Stage, a.RunID, m.Redirect(a.RunID, a.Stage, a.Note))
			}),
		tool.NewTool("cancel_run", "Cancel a run.",
			func(_ context.Context, a struct {
				RunID  string `json:"run_id" jsonschema:"description=The run id,required"`
				Reason string `json:"reason" jsonschema:"description=Optional reason"`
			}) (string, error) {
				return ack("canceled", a.RunID, m.Cancel(a.RunID, a.Reason))
			}),
	}
}

func ack(verb, runID string, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return verb + " run " + runID, nil
}
