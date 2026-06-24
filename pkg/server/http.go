// Package server is a Ring 5 edge layer: it exposes a pipeline.Manager over
// HTTP + Server-Sent Events. It is a thin adapter over the transport-agnostic
// control plane — trigger a run, observe its events live, query its state, and
// steer it (pause/resume/steer/redirect/cancel). Standard library net/http only.
package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Misakaworstlv999/goforge/pkg/pipeline"
)

// Server adapts a pipeline.Manager to HTTP.
type Server struct {
	mgr *pipeline.Manager
}

// New builds a Server over a Manager.
func New(mgr *pipeline.Manager) *Server { return &Server{mgr: mgr} }

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
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
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

func (s *Server) control(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Op     string `json:"op"`
		Stage  string `json:"stage"`
		Note   string `json:"note"`
		Reason string `json:"reason"`
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
	default:
		http.Error(w, "unknown control op: "+req.Op, http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
