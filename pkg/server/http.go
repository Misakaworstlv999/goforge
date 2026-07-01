// Package server is a Ring 5 edge layer: it exposes a pipeline.Manager over
// HTTP + Server-Sent Events. It is a thin adapter over the transport-agnostic
// control plane — trigger a run, observe its events live, query its state, and
// steer it (pause/resume/steer/redirect/cancel). Standard library net/http only.
package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Misakaworstlv999/goforge/internal/log"
	"github.com/Misakaworstlv999/goforge/pkg/pipeline"
)

// Server adapts a pipeline.Manager to HTTP.
type Server struct {
	mgr *pipeline.Manager
	log log.Logger
}

// Option configures a Server.
type Option func(*Server)

// WithLogger injects a structured logger for request lifecycle events. Without
// it the server logs nothing (no-op), so existing callers are unaffected.
func WithLogger(l log.Logger) Option { return func(s *Server) { s.log = l } }

// New builds a Server over a Manager.
func New(mgr *pipeline.Manager, opts ...Option) *Server {
	s := &Server{mgr: mgr, log: log.Nop()}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Handler returns the routed HTTP handler (Go 1.22+ method+path patterns).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /v1/runs", s.trigger)
	mux.HandleFunc("GET /v1/runs", s.list)
	mux.HandleFunc("GET /v1/runs/{id}", s.state)
	mux.HandleFunc("GET /v1/runs/{id}/events", s.events)
	mux.HandleFunc("GET /v1/runs/{id}/checkpoints", s.checkpoints)
	mux.HandleFunc("GET /v1/runs/{id}/transcript", s.transcript)
	mux.HandleFunc("POST /v1/runs/{id}/control", s.control)
	return mux
}

func (s *Server) trigger(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunID string `json:"run_id"`
		Input string `json:"input"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	id, err := s.mgr.Trigger(req.RunID, req.Input)
	if err != nil {
		s.log.Warn("trigger failed", "error", err.Error())
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	s.log.Info("run triggered", "run_id", id)
	writeJSON(w, http.StatusCreated, map[string]string{"run_id": id})
}

func (s *Server) list(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"runs": s.mgr.List()})
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	st, err := s.mgr.State(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run_id":        st.PipelineID,
		"status":        st.Status.String(),
		"current_stage": st.CurrentStage,
		"blackboard":    st.Blackboard,
	})
}

// events streams the run's events as SSE: full replay first, then live until the
// run ends or the client disconnects.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	replay, live, cancel, err := s.mgr.Subscribe(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	rc := http.NewResponseController(w)

	send := func(e pipeline.Event) bool {
		b, _ := json.Marshal(map[string]string{"type": e.Type.String(), "stage": e.Stage, "detail": e.Detail})
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return false
		}
		return rc.Flush() == nil
	}

	for _, e := range replay {
		if !send(e) {
			return
		}
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-live:
			if !ok {
				return // run finished
			}
			if !send(e) {
				return
			}
		}
	}
}

// checkpoints returns the run's checkpoint lineage — the seqs rewind/fork target.
func (s *Server) checkpoints(w http.ResponseWriter, r *http.Request) {
	cps, err := s.mgr.Checkpoints(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	out := make([]map[string]any, 0, len(cps))
	for _, c := range cps {
		out = append(out, map[string]any{
			"seq":        c.Seq,
			"stage":      c.Stage,
			"status":     c.Status.String(),
			"updated_at": c.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"checkpoints": out})
}

// transcript returns the run's reasoning transcript so a caller can audit why it
// behaved as it did. Default is structured JSON messages; ?format=text&level=
// (final|steps|full) returns a rendered plaintext view instead.
func (s *Server) transcript(w http.ResponseWriter, r *http.Request) {
	msgs, err := s.mgr.Transcript(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if r.URL.Query().Get("format") == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(pipeline.RenderTranscript(msgs, r.URL.Query().Get("level"))))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

func (s *Server) control(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Op     string `json:"op"`
		Stage  string `json:"stage"`
		Note   string `json:"note"`
		Reason string `json:"reason"`
		Seq    int    `json:"seq"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	var err error
	switch req.Op {
	case "pause":
		err = s.mgr.Pause(id)
	case "resume":
		err = s.mgr.Resume(id)
	case "steer":
		err = s.mgr.Steer(id, req.Note)
	case "redirect":
		err = s.mgr.Redirect(id, req.Stage, req.Note)
	case "cancel":
		err = s.mgr.Cancel(id, req.Reason)
	case "rewind":
		err = s.mgr.Rewind(id, req.Seq, req.Note)
	case "fork":
		var newID string
		if newID, err = s.mgr.Fork(id, "", req.Seq); err == nil {
			writeJSON(w, http.StatusCreated, map[string]string{"run_id": newID})
			return
		}
	default:
		http.Error(w, "unknown control op: "+req.Op, http.StatusBadRequest)
		return
	}
	if err != nil {
		s.log.Warn("control failed", "run_id", id, "op", req.Op, "error", err.Error())
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	s.log.Info("control applied", "run_id", id, "op", req.Op)
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
