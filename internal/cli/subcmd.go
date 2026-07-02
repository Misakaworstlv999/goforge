package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Misakaworstlv999/goforge/internal/config"
	"github.com/Misakaworstlv999/goforge/internal/log"
	"github.com/Misakaworstlv999/goforge/internal/telemetry"
	"github.com/Misakaworstlv999/goforge/pkg/agent"
	"github.com/Misakaworstlv999/goforge/pkg/llm"
	"github.com/Misakaworstlv999/goforge/pkg/pipeline"
	"github.com/Misakaworstlv999/goforge/pkg/server"
	"github.com/Misakaworstlv999/goforge/pkg/tool"
	"github.com/Misakaworstlv999/goforge/pkg/workflow"
)

// This file holds the non-interactive subcommands that expose the M7 control
// plane: serve (HTTP control plane), run (trigger + stream one run), status
// (inspect a persisted run), resume (continue a paused run). Each public handler
// builds the real dependencies (NewLLM, BuildRegistry, store, Manager) and then
// delegates to a small core (newServeServer/streamRun/printStatus/streamResume)
// that takes its inputs as parameters, so the cores are unit-testable without a
// live LLM.

const shutdownGrace = 5 * time.Second

// nopCloser is a no-op io.Closer (for the in-memory store, which needs no
// teardown), so callers can defer Close uniformly.
type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// openStore returns a checkpoint store per cfg.StorePath: a persistent SQLite
// store when a path is given (required by status/resume across processes), else
// an in-memory store. The returned io.Closer releases the SQLite handle.
func openStore(cfg config.Config) (pipeline.CheckpointStore, io.Closer, error) {
	if cfg.StorePath == "" {
		return pipeline.NewMemoryStore(), nopCloser{}, nil
	}
	s, err := pipeline.OpenSQLite(cfg.StorePath)
	if err != nil {
		return nil, nil, fmt.Errorf("opening store %q: %w", cfg.StorePath, err)
	}
	return s, s, nil
}

// buildPipeline is the single factory shared by serve/run/resume, selected by
// cfg.Pipeline. Routing all three through it means the stage graph never drifts
// between triggering a run and resuming it.
func buildPipeline(client llm.LLM, reg *tool.Registry, cfg config.Config, store pipeline.CheckpointStore) *pipeline.Pipeline {
	if cfg.Pipeline == config.PipelineDevWorkflow {
		return devWorkflowPipeline(client, reg, cfg, store)
	}
	return agentPipeline(client, reg, cfg, store)
}

// agentPipeline is the general control-plane unit of work: a single-stage ReAct
// agent over the shared LLM + tools. Each call gets a fresh blackboard so runs
// stay isolated. RunAgent auto-injects steering guidance, so steer/rewind reach
// this agent too.
func agentPipeline(client llm.LLM, reg *tool.Registry, cfg config.Config, store pipeline.CheckpointStore) *pipeline.Pipeline {
	deps := pipeline.StageDeps{LLM: client, Registry: reg, State: pipeline.NewState(), MaxAgentSteps: cfg.MaxSteps}
	p := pipeline.New(deps, pipeline.WithStore(store))
	_ = pipeline.AddStage(p, pipeline.Stage[string, string]{
		Name: "agent",
		Run: func(ctx context.Context, task string, d pipeline.StageDeps) (string, error) {
			return pipeline.RunAgent(ctx, d, task, cfg.System, agent.ContextPolicy{})
		},
	})
	return p
}

// devWorkflowPipeline builds the M6 dev-workflow graph (requirement→design→coding→
// review→tests→acceptance). A knowledge-base MCP server (cfg.KMServer, from the
// mcpServers config) is scoped onto the analysis stages when configured; empty
// KMServer runs the workflow without doc lookup.
func devWorkflowPipeline(client llm.LLM, reg *tool.Registry, cfg config.Config, store pipeline.CheckpointStore) *pipeline.Pipeline {
	deps := pipeline.StageDeps{
		LLM:           client,
		Registry:      reg,
		State:         pipeline.NewState(),
		MaxAgentSteps: cfg.MaxSteps,
	}
	return workflow.BuildDevWorkflow(deps, workflow.Config{KMMCPServer: cfg.KMServer}, pipeline.WithStore(store))
}

func closeAll(cs []io.Closer) {
	for _, c := range cs {
		_ = c.Close()
	}
}

// storeKind describes the configured store for a log field.
func storeKind(cfg config.Config) string {
	if cfg.StorePath == "" {
		return "memory"
	}
	return "sqlite:" + cfg.StorePath
}

// printEvent renders one pipeline event as a console line.
func printEvent(w io.Writer, e pipeline.Event) {
	fmt.Fprintf(w, "%-14s %-10s %s\n", e.Type.String(), e.Stage, e.Detail)
}

// initTelemetry wires OTLP export per cfg and returns a shutdown func. With no
// endpoint configured it is a no-op (telemetry stays disabled, zero overhead).
func initTelemetry(cfg config.Config) (func(context.Context) error, error) {
	bodyMode, err := telemetry.ParseBodyCapture(cfg.OTelBody)
	if err != nil {
		return nil, err
	}
	return telemetry.Init(context.Background(), telemetry.Options{
		Endpoint:     cfg.OTelEndpoint,
		Insecure:     cfg.OTelInsecure,
		ServiceName:  cfg.ServiceName,
		BodyCapture:  bodyMode,
		BodyMaxBytes: cfg.OTelBodyMaxBytes,
	})
}

// --- serve ---

// newServeServer builds the HTTP control-plane server and a cleanup func without
// starting it — the testable wiring seam (the returned Handler can be mounted on
// httptest without a live LLM, since the LLM is only touched once a run executes).
func newServeServer(cfg config.Config, logger log.Logger) (*http.Server, func(), error) {
	client := NewLLM(cfg)
	reg, closers := BuildRegistry(context.Background(), cfg, io.Discard)
	store, storeCloser, err := openStore(cfg)
	if err != nil {
		closeAll(closers)
		return nil, nil, err
	}
	mgr := pipeline.NewManager(func(string) *pipeline.Pipeline {
		return buildPipeline(client, reg, cfg, store)
	})
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           server.New(mgr, server.WithLogger(logger)).Handler(),
		ReadHeaderTimeout: shutdownGrace,
	}
	cleanup := func() {
		mgr.Close()
		_ = storeCloser.Close()
		closeAll(closers)
	}
	return srv, cleanup, nil
}

// Serve starts the HTTP control plane, blocking until SIGINT/SIGTERM, then
// draining gracefully.
func Serve(args []string) int {
	cfg, err := config.Parse(args, os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	logger := log.New(cfg.LogLevel, cfg.LogFormat, os.Stderr)

	shutdown, err := initTelemetry(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "telemetry:", err)
		return 1
	}
	defer func() { _ = shutdown(context.Background()) }()

	srv, cleanup, err := newServeServer(cfg, logger)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer cleanup()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	logger.Info("serve listening", "addr", cfg.HTTPAddr, "store", storeKind(cfg))

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("serve failed", "error", err.Error())
			return 1
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	return 0
}

// --- run ---

// streamRun triggers a run for task and streams its events to out until the run
// finishes. The Manager is injected so it is testable with a non-LLM pipeline.
func streamRun(mgr *pipeline.Manager, task string, out, errOut io.Writer) int {
	id, err := mgr.Trigger("", task)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	fmt.Fprintf(out, "run %s\n", id)

	replay, live, cancel, err := mgr.Subscribe(id)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	defer cancel()
	for _, e := range replay {
		printEvent(out, e)
	}
	for e := range live {
		printEvent(out, e)
	}
	return 0
}

// RunCmd triggers one run from the positional task and streams its events.
func RunCmd(args []string) int {
	cfg, rest, err := config.ParseArgs(args, os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	task := strings.TrimSpace(strings.Join(rest, " "))
	if task == "" {
		fmt.Fprintln(os.Stderr, "usage: goforge run [flags] <task>")
		return 2
	}

	shutdown, err := initTelemetry(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "telemetry:", err)
		return 1
	}
	defer func() { _ = shutdown(context.Background()) }()

	client := NewLLM(cfg)
	reg, closers := BuildRegistry(context.Background(), cfg, os.Stderr)
	defer closeAll(closers)
	store, storeCloser, err := openStore(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = storeCloser.Close() }()

	mgr := pipeline.NewManager(func(string) *pipeline.Pipeline {
		return buildPipeline(client, reg, cfg, store)
	})
	defer mgr.Close()
	return streamRun(mgr, task, os.Stdout, os.Stderr)
}

// --- status ---

// printStatus writes a persisted run's state, checkpoint lineage, and audit
// trail to out. Store is injected so it is testable against a seeded store.
func printStatus(store pipeline.CheckpointStore, id string, out, errOut io.Writer) int {
	ctx := context.Background()
	st, err := store.Load(ctx, id)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	fmt.Fprintf(out, "run %s\n  status: %s\n  stage:  %s\n  seq:    %d\n",
		st.PipelineID, st.Status.String(), st.CurrentStage, st.Seq)

	if cps, err := store.ListCheckpoints(ctx, id); err == nil && len(cps) > 0 {
		fmt.Fprintln(out, "  checkpoints:")
		for _, c := range cps {
			fmt.Fprintf(out, "    seq=%d stage=%s status=%s\n", c.Seq, c.Stage, c.Status.String())
		}
	}
	if al, err := store.AuditLog(ctx, id); err == nil && len(al) > 0 {
		fmt.Fprintln(out, "  audit:")
		for _, a := range al {
			fmt.Fprintf(out, "    %s %s %s %s\n", a.Timestamp.Format(time.RFC3339), a.Stage, a.Action, a.Detail)
		}
	}
	return 0
}

// Status inspects a persisted run. Requires -store.
func Status(args []string) int {
	cfg, rest, err := config.ParseArgs(args, os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if cfg.StorePath == "" {
		fmt.Fprintln(os.Stderr, "status requires a persistent store: pass -store <path> (or GOFORGE_STORE)")
		return 2
	}
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "usage: goforge status -store <path> <run-id>")
		return 2
	}
	store, err := pipeline.OpenSQLite(cfg.StorePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = store.Close() }()
	return printStatus(store, rest[0], os.Stdout, os.Stderr)
}

// --- resume ---

// streamResume continues a paused run and streams its events. The pipeline is
// injected so it is testable with a non-LLM stage.
func streamResume(p *pipeline.Pipeline, id string, approved bool, out, errOut io.Writer) int {
	for ev, err := range p.Resume(context.Background(), id, pipeline.Decision{Approved: approved}) {
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		printEvent(out, ev)
	}
	return 0
}

// Resume continues a paused run from the store. Requires -store. By default it
// approves the paused gate; -reject retries the stage instead.
func Resume(args []string) int {
	cfg, rest, err := config.ParseArgs(args, os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if cfg.StorePath == "" {
		fmt.Fprintln(os.Stderr, "resume requires a persistent store: pass -store <path> (or GOFORGE_STORE)")
		return 2
	}
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "usage: goforge resume -store <path> <run-id>")
		return 2
	}
	store, err := pipeline.OpenSQLite(cfg.StorePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = store.Close() }()

	client := NewLLM(cfg)
	reg, closers := BuildRegistry(context.Background(), cfg, os.Stderr)
	defer closeAll(closers)

	p := buildPipeline(client, reg, cfg, store)
	return streamResume(p, rest[0], true, os.Stdout, os.Stderr)
}
