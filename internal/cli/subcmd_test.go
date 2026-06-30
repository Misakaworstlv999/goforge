package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Misakaworstlv999/goforge/internal/config"
	"github.com/Misakaworstlv999/goforge/pkg/pipeline"
)

// echoPipeline is a non-LLM stand-in for the agent pipeline: one stage that
// returns its input. It lets the CLI cores be tested without a live model.
func echoPipeline(store pipeline.CheckpointStore) *pipeline.Pipeline {
	p := pipeline.New(pipeline.StageDeps{State: pipeline.NewState()}, pipeline.WithStore(store))
	_ = pipeline.AddStage(p, pipeline.Stage[string, string]{
		Name: "echo",
		Run:  func(_ context.Context, in string, _ pipeline.StageDeps) (string, error) { return in, nil },
	})
	return p
}

func TestStreamRun_echo(t *testing.T) {
	store := pipeline.NewMemoryStore()
	mgr := pipeline.NewManager(func(string) *pipeline.Pipeline { return echoPipeline(store) })
	defer mgr.Close()

	var out, errOut bytes.Buffer
	if code := streamRun(mgr, "hello", &out, &errOut); code != 0 {
		t.Fatalf("streamRun code=%d, stderr=%s", code, errOut.String())
	}
	s := out.String()
	if !strings.Contains(s, "run ") {
		t.Errorf("output missing run id:\n%s", s)
	}
	if !strings.Contains(s, "done") {
		t.Errorf("output missing done event:\n%s", s)
	}
}

func TestPrintStatus_seededAndMissing(t *testing.T) {
	store := pipeline.NewMemoryStore()
	ctx := context.Background()
	st := &pipeline.PipelineState{PipelineID: "r1", Status: pipeline.StatusCompleted, CurrentStage: "agent", Seq: 2}
	if err := store.Save(ctx, st); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveStep(ctx, st); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if code := printStatus(store, "r1", &out, &errOut); code != 0 {
		t.Fatalf("printStatus code=%d, stderr=%s", code, errOut.String())
	}
	s := out.String()
	if !strings.Contains(s, "status: completed") || !strings.Contains(s, "seq:") {
		t.Errorf("status output missing fields:\n%s", s)
	}
	if !strings.Contains(s, "checkpoints:") {
		t.Errorf("status output missing checkpoint lineage:\n%s", s)
	}

	var o2, e2 bytes.Buffer
	if code := printStatus(store, "missing", &o2, &e2); code != 1 {
		t.Errorf("printStatus(missing) code=%d, want 1", code)
	}
}

func TestStreamResume_pausedEcho(t *testing.T) {
	store := pipeline.NewMemoryStore()
	build := func() *pipeline.Pipeline {
		p := pipeline.New(pipeline.StageDeps{State: pipeline.NewState()}, pipeline.WithStore(store))
		_ = pipeline.AddStage(p, pipeline.Stage[string, string]{
			Name: "echo",
			Run:  func(_ context.Context, in string, _ pipeline.StageDeps) (string, error) { return in, nil },
			Gate: pipeline.HumanGate(),
		})
		return p
	}
	// First run pauses at the human gate and persists.
	for _, err := range build().Run(context.Background(), "r1", "hi") {
		if err != nil {
			t.Fatal(err)
		}
	}
	// Resuming with approval routes past the gate to completion.
	var out, errOut bytes.Buffer
	if code := streamResume(build(), "r1", true, &out, &errOut); code != 0 {
		t.Fatalf("streamResume code=%d, stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "done") {
		t.Errorf("resume did not reach done:\n%s", out.String())
	}
}

func TestNewServeServer_healthzAndRoutes(t *testing.T) {
	// Point MCP config at a nonexistent path so BuildRegistry registers no servers.
	cfg := config.Config{
		Provider:      "openai",
		MCPConfigPath: filepath.Join(t.TempDir(), "no-such.json"),
		MCPExpose:     config.MCPExposeDirect,
		HTTPAddr:      ":0",
	}
	srv, cleanup, err := newServeServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	if body := httpGet(t, ts.URL+"/healthz"); body != "ok" {
		t.Errorf("/healthz = %q, want ok", body)
	}
	if body := httpGet(t, ts.URL+"/v1/runs"); !strings.Contains(body, "runs") {
		t.Errorf("/v1/runs = %q, want a runs field", body)
	}
}

func TestSubcommands_usageAndStoreGuards(t *testing.T) {
	if code := RunCmd(nil); code != 2 { // empty task
		t.Errorf("RunCmd(nil) = %d, want 2", code)
	}
	if code := Status(nil); code != 2 { // missing -store
		t.Errorf("Status(nil) = %d, want 2", code)
	}
	if code := Resume(nil); code != 2 { // missing -store
		t.Errorf("Resume(nil) = %d, want 2", code)
	}
	if code := Status([]string{"-store", filepath.Join(t.TempDir(), "s.db")}); code != 2 { // store but no run id
		t.Errorf("Status(store only) = %d, want 2", code)
	}
}

func httpGet(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return strings.TrimSpace(string(b))
}
