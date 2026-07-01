package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Misakaworstlv999/goforge/pkg/llm"
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

		tool.NewTool("get_run_transcript", "Read a run's reasoning transcript (the LLM's step-by-step messages: reasoning, tool calls, tool results) so you can audit WHY it behaved as it did before steering/redirecting/rewinding it. level tunes verbosity like a log level: final (just the last answer) | steps (reasoning + tool names + result status; default) | full (everything verbatim, including tool args and result bodies).",
			func(ctx context.Context, a struct {
				RunID string `json:"run_id" jsonschema:"description=The run id,required"`
				Level string `json:"level" jsonschema:"description=Detail level: final | steps | full (default steps)"`
			}) (string, error) {
				msgs, err := m.Transcript(ctx, a.RunID)
				if err != nil {
					return "", err
				}
				if len(msgs) == 0 {
					return "(no transcript yet: the run has no store, or has not produced any messages)", nil
				}
				return RenderTranscript(msgs, a.Level), nil
			}),

		tool.NewTool("list_checkpoints", "List a run's checkpoint lineage (seq, stage, status) — the points rewind/fork can target.",
			func(ctx context.Context, a runID) (string, error) {
				cps, err := m.Checkpoints(ctx, a.RunID)
				if err != nil {
					return "", err
				}
				if len(cps) == 0 {
					return "(no checkpoints)", nil
				}
				var b strings.Builder
				for _, c := range cps {
					fmt.Fprintf(&b, "seq=%d stage=%s status=%s\n", c.Seq, c.Stage, c.Status.String())
				}
				return b.String(), nil
			}),
		tool.NewTool("rewind_run", "Re-run a run from an earlier checkpoint (time-travel, same id), optionally injecting guidance. Use list_checkpoints to pick a seq.",
			func(_ context.Context, a struct {
				RunID string `json:"run_id" jsonschema:"description=The run id,required"`
				Seq   int    `json:"seq" jsonschema:"description=Checkpoint seq to rewind to,required"`
				Note  string `json:"note" jsonschema:"description=Optional guidance to inject"`
			}) (string, error) {
				return ack(fmt.Sprintf("rewound to seq %d", a.Seq), a.RunID, m.Rewind(a.RunID, a.Seq, a.Note))
			}),
		tool.NewTool("fork_run", "Start an independent new run continuing from an earlier checkpoint of a run; returns the new run id.",
			func(_ context.Context, a struct {
				RunID string `json:"run_id" jsonschema:"description=The source run id,required"`
				Seq   int    `json:"seq" jsonschema:"description=Checkpoint seq to fork from,required"`
			}) (string, error) {
				newID, err := m.Fork(a.RunID, "", a.Seq)
				if err != nil {
					return "", err
				}
				return "forked run " + a.RunID + " → " + newID, nil
			}),
	}
}

func ack(verb, runID string, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return verb + " run " + runID, nil
}

// RenderTranscript formats a durable reasoning transcript for a controller to
// audit. level tunes verbosity like a log level:
//   - "final": only the last assistant message (the answer).
//   - "steps" (default): the task, each assistant reasoning turn, tool-call
//     names, and tool-result status/preview — the "why" without the bulk.
//   - "full": everything verbatim, including the system prompt, tool-call
//     arguments, and full tool-result bodies.
//
// An unknown level is treated as "steps".
func RenderTranscript(msgs []llm.Message, level string) string {
	switch level {
	case "final", "steps", "full":
	default:
		level = "steps"
	}

	if level == "final" {
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == llm.RoleAssistant && msgs[i].Content != "" {
				return msgs[i].Content
			}
		}
		return "(no final assistant message)"
	}

	var b strings.Builder
	for _, msg := range msgs {
		switch msg.Role {
		case llm.RoleSystem:
			if level == "full" {
				fmt.Fprintf(&b, "[system] %s\n", msg.Content)
			}
		case llm.RoleUser:
			fmt.Fprintf(&b, "[user] %s\n", clip(msg.Content, level))
		case llm.RoleAssistant:
			if msg.Content != "" {
				fmt.Fprintf(&b, "[assistant] %s\n", msg.Content)
			}
			for _, tc := range msg.ToolCalls {
				if level == "full" {
					fmt.Fprintf(&b, "  → tool_call %s(%s)\n", tc.Name, tc.Args)
				} else {
					fmt.Fprintf(&b, "  → tool_call %s\n", tc.Name)
				}
			}
		case llm.RoleTool:
			status := "ok"
			if msg.IsError {
				status = "ERROR"
			}
			fmt.Fprintf(&b, "  ← tool_result [%s] %s\n", status, clip(msg.Content, level))
		}
	}
	return b.String()
}

// clip returns s verbatim at "full" level, otherwise a truncated preview.
func clip(s, level string) string {
	if level == "full" {
		return s
	}
	return describe(s) // describe truncates to a readable preview (…)
}
