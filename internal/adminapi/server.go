// Package adminapi exposes an HTTP-over-UDS admin surface for a running
// slackrun daemon: list in-flight runs and kill them. The socket is
// per-user (0600, path in $XDG_RUNTIME_DIR or $TMPDIR); the design
// deliberately does *not* speak TCP. Trust boundary: any process running
// as the same OS user can connect.
package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kohii/slackrun/internal/logging"
	"github.com/kohii/slackrun/internal/runmgr"
)

// APIVersion is bumped when the wire format changes incompatibly. The URL
// prefix mirrors it so a stale CLI can detect version skew.
const APIVersion = "v1"

// bootTime is used by /v1/health. Populated by New.
type Server struct {
	runs    *runmgr.Manager
	http    *http.Server
	listen  net.Listener
	sockPth string
	boot    time.Time
	version string

	stopOnce sync.Once
}

// Options configures the admin server.
type Options struct {
	// Runs is the shared run manager (App.Runs()). Required.
	Runs *runmgr.Manager
	// Version is surfaced by /v1/health so clients can pin behaviour.
	Version string
}

// New returns a Server. It does not open the socket; call Start.
func New(opts Options) *Server {
	return &Server{
		runs:    opts.Runs,
		boot:    time.Now(),
		version: opts.Version,
	}
}

// Start resolves the socket path, listens, and begins serving. Returns
// ErrDisabled when the operator has opted out (SLACKRUN_ADMIN_SOCKET=off);
// callers treat that as "just skip". Any other error is fatal for the
// admin surface but should not halt the main dispatcher.
func (s *Server) Start() error {
	path, err := ResolveSocketPath()
	if err != nil {
		return err
	}
	if err := PrepareSocket(path); err != nil {
		return err
	}
	ln, err := ListenUnix(path)
	if err != nil {
		return err
	}
	s.listen = ln
	s.sockPth = path

	mux := http.NewServeMux()
	s.registerRoutes(mux)
	s.http = &http.Server{
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 15 * time.Second,
	}
	logging.Info("admin api listening", logging.F("socket", path))
	go func() {
		if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logging.Warn("admin api serve exited", logging.F("error", err))
		}
	}()
	return nil
}

// Stop gracefully drains, closes the listener (which also unlinks the
// socket file on unix), and is idempotent. Bounded by ctx.
func (s *Server) Stop(ctx context.Context) {
	s.stopOnce.Do(func() {
		if s.http != nil {
			_ = s.http.Shutdown(ctx)
		}
		// net.Listen("unix", ...).Close() unlinks the socket file, but we
		// double-tap in case Shutdown already closed it.
		if s.listen != nil {
			_ = s.listen.Close()
		}
	})
}

// SocketPath returns the resolved socket path, mostly for logs. Empty
// before Start has been called.
func (s *Server) SocketPath() string { return s.sockPth }

// registerRoutes wires up the v1 endpoints. Kept in one place so the API
// surface can be eyeballed at a glance.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/"+APIVersion+"/health", s.handleHealth)
	mux.HandleFunc("/"+APIVersion+"/runs", s.handleRuns)
	// Sub-paths under /v1/runs/ (single kill, kill-all).
	mux.HandleFunc("/"+APIVersion+"/runs/", s.handleRunsSub)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"version":     s.version,
		"uptime_ms":   time.Since(s.boot).Milliseconds(),
		"active_runs": len(s.runs.Snapshot()),
	})
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only")
		return
	}
	snaps := s.runs.Snapshot()
	out := make([]runView, 0, len(snaps))
	for _, s := range snaps {
		out = append(out, snapshotToView(s))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRunsSub dispatches /v1/runs/{id}/kill and /v1/runs/kill-all. Kept
// hand-rolled rather than pulling in a router — the surface is tiny.
func (s *Server) handleRunsSub(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/"+APIVersion+"/runs/")
	if rest == "kill-all" {
		s.handleKillAll(w, r)
		return
	}
	if strings.HasSuffix(rest, "/kill") {
		id := strings.TrimSuffix(rest, "/kill")
		s.handleKill(w, r, id)
		return
	}
	writeErr(w, http.StatusNotFound, "not_found", "no such endpoint")
}

func (s *Server) handleKill(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
		return
	}
	if id == "" || strings.ContainsRune(id, '/') {
		writeErr(w, http.StatusBadRequest, "bad_id", "id required")
		return
	}
	var body killRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_json", err.Error())
			return
		}
	}
	res, err := s.runs.Kill(id, runmgr.KillOptions{Reason: body.Reason})
	switch {
	case errors.Is(err, runmgr.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not_found", "no such run")
		return
	case errors.Is(err, runmgr.ErrNotKillable):
		writeErr(w, http.StatusConflict, "not_killable", "run is not in a killable state")
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"killed":  true,
		"id":      res.ID,
		"full_id": res.FullID,
	})
}

func (s *Server) handleKillAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
		return
	}
	var body killRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_json", err.Error())
			return
		}
	}
	results := s.runs.KillAll(runmgr.KillOptions{Reason: body.Reason})
	ids := make([]string, 0, len(results))
	for _, r := range results {
		ids = append(ids, r.ID)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"killed":     true,
		"killed_ids": ids,
	})
}

// killRequest is the JSON body accepted by both single-kill and kill-all.
type killRequest struct {
	Reason string `json:"reason,omitempty"`
}

// runView is the JSON shape returned by GET /v1/runs. Kept separate from
// runmgr.Snapshot so we can control naming and hide zero-value fields
// (json:",omitempty") without polluting the domain type.
type runView struct {
	ID         string `json:"id"`
	FullID     string `json:"full_id"`
	Rule       string `json:"rule"`
	ChannelID  string `json:"channel_id"`
	UserID     string `json:"user_id,omitempty"`
	ThreadTS   string `json:"thread_ts,omitempty"`
	StartedAt  string `json:"started_at"`
	ElapsedMs  int64  `json:"elapsed_ms"`
	State      string `json:"state"`
	PID        int    `json:"pid,omitempty"`
	ExitCode   int    `json:"exit_code,omitempty"`
	ExitCause  string `json:"exit_cause,omitempty"`
	KillReason string `json:"kill_reason,omitempty"`
}

func snapshotToView(s runmgr.Snapshot) runView {
	view := runView{
		ID:         s.ID,
		FullID:     s.FullID,
		Rule:       s.RuleName,
		ChannelID:  s.ChannelID,
		UserID:     s.UserID,
		ThreadTS:   s.ThreadTS,
		StartedAt:  s.StartedAt.UTC().Format(time.RFC3339),
		ElapsedMs:  time.Since(s.StartedAt).Milliseconds(),
		State:      s.State.String(),
		PID:        s.PID,
		ExitCode:   s.ExitCode,
		KillReason: s.KillReason,
	}
	if s.ExitCause != runmgr.CauseNone {
		view.ExitCause = s.ExitCause.String()
	}
	return view
}

// writeJSON serialises v to w with the given status. Unmarshallable inputs
// are unreachable in this package (all handlers use concrete types), so we
// don't bother threading the error back.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type errBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	var b errBody
	b.Error.Code = code
	b.Error.Message = msg
	writeJSON(w, status, b)
}
