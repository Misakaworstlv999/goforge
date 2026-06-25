package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Misakaworstlv999/goforge/pkg/pipeline"
)

// fixture builds a Manager whose runs are a 2-stage pass-through pipeline sharing
// a store; gate (if non-nil) blocks the first stage until closed.
func fixture(t *testing.T, gate <-chan struct{}) (*pipeline.Manager, *pipeline.MemoryStore) {
	t.Helper()
	store := pipeline.NewMemoryStore()
	factory := func(id string) *pipeline.Pipeline {
		p := pipeline.New(pipeline.StageDeps{State: pipeline.NewState()}, pipeline.WithStore(store))
		if err := pipeline.AddStage(p, pipeline.Stage[string, string]{
			Name: "a",
			Run: func(_ context.Context, in string, _ pipeline.StageDeps) (string, error) {
				if gate != nil {
					<-gate
				}
				return in, nil
			},
		}); err != nil {
			t.Fatal(err)
		}
		if err := pipeline.AddStage(p, pipeline.Stage[string, string]{
			Name: "b",
			Run:  func(_ context.Context, in string, _ pipeline.StageDeps) (string, error) { return in, nil },
		}); err != nil {
			t.Fatal(err)
		}
		return p
	}
	return pipeline.NewManager(factory), store
}

func TestServer_triggerStateEventsHealth(t *testing.T) {
	mgr, _ := fixture(t, nil)
	srv := httptest.NewServer(New(mgr).Handler())
	defer srv.Close()

	// health
	if body := get(t, srv.URL+"/healthz"); body != "ok" {
		t.Errorf("healthz = %q", body)
	}

	// trigger
	post(t, srv.URL+"/v1/runs", `{"run_id":"r1","input":"hello"}`)
	mgr.Wait("r1")

	// state
	if st := get(t, srv.URL+"/v1/runs/r1"); !strings.Contains(st, `"status":"completed"`) {
		t.Errorf("state = %q, want completed", st)
	}
	// events (SSE; run done ⇒ replay then EOF)
	if ev := get(t, srv.URL+"/v1/runs/r1/events"); !strings.Contains(ev, `"type":"done"`) {
		t.Errorf("events = %q, want a done event", ev)
	}
}

func TestServer_controlCancel(t *testing.T) {
	gate := make(chan struct{})
	mgr, store := fixture(t, gate)
	srv := httptest.NewServer(New(mgr).Handler())
	defer srv.Close()

	post(t, srv.URL+"/v1/runs", `{"run_id":"c1"}`) // blocks in stage "a"
	// cancel queued; release the stage so the next safe point applies it
	postExpect(t, srv.URL+"/v1/runs/c1/control", `{"op":"cancel","reason":"stop"}`, http.StatusNoContent)
	close(gate)
	mgr.Wait("c1")

	st, _ := store.Load(context.Background(), "c1")
	if st.Status != pipeline.StatusCanceled {
		t.Errorf("status = %v, want canceled", st.Status)
	}
}

func TestServer_checkpointsRewindFork(t *testing.T) {
	mgr, _ := fixture(t, nil)
	srv := httptest.NewServer(New(mgr).Handler())
	defer srv.Close()

	post(t, srv.URL+"/v1/runs", `{"run_id":"tt","input":"hello"}`)
	mgr.Wait("tt")

	// lineage is observable
	if cps := get(t, srv.URL+"/v1/runs/tt/checkpoints"); !strings.Contains(cps, `"seq"`) {
		t.Errorf("checkpoints = %q, want a seq-bearing lineage", cps)
	}

	// rewind same id (time-travel) → 204
	postExpect(t, srv.URL+"/v1/runs/tt/control", `{"op":"rewind","seq":1,"note":"redo"}`, http.StatusNoContent)
	mgr.Wait("tt")

	// fork → 201 + new run id, which completes independently
	newID := postJSON(t, srv.URL+"/v1/runs/tt/control", `{"op":"fork","seq":1}`, http.StatusCreated)
	if newID == "" {
		t.Fatal("fork returned no run_id")
	}
	mgr.Wait(newID)
	if st := get(t, srv.URL+"/v1/runs/"+newID); !strings.Contains(st, `"status":"completed"`) {
		t.Errorf("forked run state = %q, want completed", st)
	}
}

// --- helpers ---

// postJSON posts body, asserts status, and returns the "run_id" field of the
// JSON response (if any).
func postJSON(t *testing.T, url, body string, wantStatus int) string {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("POST %s: status %d, want %d (%s)", url, resp.StatusCode, wantStatus, b)
	}
	var out struct {
		RunID string `json:"run_id"`
	}
	_ = json.Unmarshal(b, &out)
	return out.RunID
}

func get(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return strings.TrimSpace(string(b))
}

func post(t *testing.T, url, body string) {
	t.Helper()
	postExpect(t, url, body, -1)
}

func postExpect(t *testing.T, url, body string, wantStatus int) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if wantStatus > 0 && resp.StatusCode != wantStatus {
		t.Fatalf("POST %s: status %d, want %d", url, resp.StatusCode, wantStatus)
	}
}
