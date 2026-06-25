package pipeline

import (
	"context"
	"iter"
	"sync/atomic"
	"testing"

	"github.com/Misakaworstlv999/goforge/pkg/agent"
	"github.com/Misakaworstlv999/goforge/pkg/llm"
	"github.com/Misakaworstlv999/goforge/pkg/tool"
)

// toolLoopLLM always asks to call the "wait" tool, so an agent driven by it loops
// indefinitely (until maxSteps) — letting a test prove control interrupts it at a
// STEP boundary rather than only after the whole stage. It counts model calls.
type toolLoopLLM struct{ calls int32 }

func (m *toolLoopLLM) Chat(context.Context, []llm.Message, ...llm.Option) (*llm.Response, error) {
	atomic.AddInt32(&m.calls, 1)
	return &llm.Response{
		Message:    llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c1", Name: "wait", Args: "{}"}}},
		StopReason: llm.StopReasonToolCall,
		Usage:      llm.Usage{TotalTokens: 1},
	}, nil
}

func (m *toolLoopLLM) ChatStream(context.Context, []llm.Message, ...llm.Option) iter.Seq2[llm.Chunk, error] {
	return func(yield func(llm.Chunk, error) bool) { yield(llm.Chunk{StopReason: llm.StopReasonEnd}, nil) }
}

// TestAgentControl_cancelMidAgent proves a controller cancels at the agent's next
// reasoning-step safe point, not only between stages: the agent makes exactly one
// model call (step 0), blocks in its tool, and aborts at step 1 once cancel is
// queued — far below its maxSteps.
func TestAgentControl_cancelMidAgent(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	llmc := &toolLoopLLM{}
	reg := tool.NewRegistry()
	must(t, reg.Register(tool.NewTool("wait", "blocks until released",
		func(ctx context.Context, _ struct{}) (string, error) {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
			return "done", nil
		})))
	store := NewMemoryStore()

	factory := func(string) *Pipeline {
		p := New(StageDeps{LLM: llmc, Registry: reg, State: NewState()}, WithStore(store))
		must(t, AddStage(p, Stage[string, string]{
			Name: "work",
			Run: func(ctx context.Context, _ string, d StageDeps) (string, error) {
				return RunAgent(ctx, d, "loop forever", "you loop", agent.ContextPolicy{})
			},
		}))
		return p
	}
	mgr := NewManager(factory)

	id, err := mgr.Trigger("agent-1", "x")
	if err != nil {
		t.Fatal(err)
	}
	<-entered                           // step 0 done: one model call, now inside the tool
	must(t, mgr.Cancel(id, "stop now")) // queue cancel
	close(release)                      // tool returns → step 1 safe point applies cancel
	mgr.Wait(id)

	if got := atomic.LoadInt32(&llmc.calls); got != 1 {
		t.Errorf("model calls = %d, want 1 (canceled at the step-1 safe point, not after maxSteps)", got)
	}
	st, _ := store.Load(context.Background(), id)
	if st.Status != StatusCanceled {
		t.Errorf("status = %v, want canceled", st.Status)
	}
}

// TestAgentControl_steerMidAgent proves steer injects guidance into the agent's
// conversation between steps (the model sees it on its next call).
func TestAgentControl_steerMidAgent(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var sawSteer atomic.Bool
	llmc := &steerWatchLLM{saw: &sawSteer}
	reg := tool.NewRegistry()
	must(t, reg.Register(tool.NewTool("wait", "blocks once",
		func(ctx context.Context, _ struct{}) (string, error) {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
			return "done", nil
		})))

	factory := func(string) *Pipeline {
		p := New(StageDeps{LLM: llmc, Registry: reg, State: NewState()})
		must(t, AddStage(p, Stage[string, string]{
			Name: "work",
			Run: func(ctx context.Context, _ string, d StageDeps) (string, error) {
				return RunAgent(ctx, d, "task", "sys", agent.ContextPolicy{})
			},
		}))
		return p
	}
	mgr := NewManager(factory)
	id, _ := mgr.Trigger("steer-agent", "x")
	<-entered
	must(t, mgr.Steer(id, "use approach Z"))
	close(release) // step 1: interrupt injects the steer message before the next model call
	mgr.Wait(id)
	if !sawSteer.Load() {
		t.Error("expected the steer note to reach the agent's conversation on its next model call")
	}
}

// steerWatchLLM calls the wait tool on step 0, then on the next step records
// whether a steer message reached the conversation and finishes.
type steerWatchLLM struct {
	calls int32
	saw   *atomic.Bool
}

func (m *steerWatchLLM) Chat(_ context.Context, msgs []llm.Message, _ ...llm.Option) (*llm.Response, error) {
	n := atomic.AddInt32(&m.calls, 1)
	if n == 1 {
		return &llm.Response{
			Message:    llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c1", Name: "wait", Args: "{}"}}},
			StopReason: llm.StopReasonToolCall,
			Usage:      llm.Usage{TotalTokens: 1},
		}, nil
	}
	for _, msg := range msgs {
		if msg.Role == llm.RoleUser && len(msg.Content) >= 7 && msg.Content[:7] == "[steer]" {
			m.saw.Store(true)
		}
	}
	return &llm.Response{Message: llm.AssistantMessage("done"), StopReason: llm.StopReasonEnd, Usage: llm.Usage{TotalTokens: 1}}, nil
}

func (m *steerWatchLLM) ChatStream(context.Context, []llm.Message, ...llm.Option) iter.Seq2[llm.Chunk, error] {
	return func(yield func(llm.Chunk, error) bool) { yield(llm.Chunk{StopReason: llm.StopReasonEnd}, nil) }
}
