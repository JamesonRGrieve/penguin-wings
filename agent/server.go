// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/gorilla/websocket"
)

// Server exposes the supervisor over an authenticated HTTP + websocket API for
// Wings to drive console, logs, exit state, and stats.
type Server struct {
	sup      *Supervisor
	buf      *LineBuffer
	token    string
	upgrader websocket.Upgrader
}

// NewServer wires a supervisor + buffer behind a bearer-token-protected API.
func NewServer(sup *Supervisor, buf *LineBuffer, token string) *Server {
	return &Server{
		sup:   sup,
		buf:   buf,
		token: token,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
}

// Handler returns the routed, auth-wrapped HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(PathStart, s.auth(s.handleStart))
	mux.HandleFunc(PathStop, s.auth(s.handleStop))
	mux.HandleFunc(PathSignal, s.auth(s.handleSignal))
	mux.HandleFunc(PathStdin, s.auth(s.handleStdin))
	mux.HandleFunc(PathLogs, s.auth(s.handleLogs))
	mux.HandleFunc(PathExit, s.auth(s.handleExit))
	mux.HandleFunc(PathStats, s.auth(s.handleStats))
	mux.HandleFunc(PathConsole, s.auth(s.handleConsole))
	return mux
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) authorized(r *http.Request) bool {
	if s.token == "" {
		return false
	}
	tok := r.URL.Query().Get(QueryToken)
	if h := r.Header.Get(HeaderAuth); strings.HasPrefix(h, AuthScheme) {
		tok = strings.TrimPrefix(h, AuthScheme)
	}
	return subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) == 1
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	var req StartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Command) == "" {
		http.Error(w, "command is required", http.StatusBadRequest)
		return
	}
	err := s.sup.Start(context.Background(), "sh", []string{"-c", req.Command}, req.Cwd, req.Env)
	switch {
	case errors.Is(err, ErrAlreadyRunning):
		http.Error(w, err.Error(), http.StatusConflict)
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if err := s.sup.Stop(); err != nil && !errors.Is(err, ErrNotRunning) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSignal(w http.ResponseWriter, r *http.Request) {
	var req SignalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sig, ok := signalFromName(req.Signal)
	if !ok {
		http.Error(w, fmt.Sprintf("unknown signal %q", req.Signal), http.StatusBadRequest)
		return
	}
	if err := s.sup.Signal(sig); err != nil && !errors.Is(err, ErrNotRunning) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleStdin(w http.ResponseWriter, r *http.Request) {
	var req StdinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.sup.WriteStdin(req.Data); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	n := 0
	if v := r.URL.Query().Get(QueryLines); v != "" {
		n, _ = strconv.Atoi(v)
	}
	writeJSON(w, LogsResponse{Lines: s.buf.Lines(n)})
}

func (s *Server) handleExit(w http.ResponseWriter, r *http.Request) {
	exited, code, oom := s.sup.Exit()
	writeJSON(w, ExitResponse{Exited: exited, Code: code, OOM: oom})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, StatsResponse{Running: s.sup.Running(), MemoryBytes: readRSS(s.sup.Pid())})
}

func (s *Server) handleConsole(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Send the backlog, then stream live output.
	for _, line := range s.buf.Lines(0) {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(line)); err != nil {
			return
		}
	}
	id, ch := s.buf.Subscribe()
	defer s.buf.Unsubscribe(id)

	// Inbound messages become stdin lines.
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				conn.Close()
				return
			}
			_ = s.sup.WriteStdin(string(msg) + consoleNewl)
		}
	}()

	for line := range ch {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(line)); err != nil {
			return
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func signalFromName(name string) (os.Signal, bool) {
	switch strings.ToUpper(strings.TrimPrefix(strings.ToUpper(name), "SIG")) {
	case "TERM":
		return syscall.SIGTERM, true
	case "KILL":
		return syscall.SIGKILL, true
	case "INT":
		return syscall.SIGINT, true
	case "HUP":
		return syscall.SIGHUP, true
	case "QUIT":
		return syscall.SIGQUIT, true
	}
	return nil, false
}

// readRSS reads the resident set size (bytes) of pid from /proc, or 0.
func readRSS(pid int) int64 {
	if pid <= 0 {
		return 0
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, _ := strconv.ParseInt(fields[1], 10, 64)
				return kb * 1024
			}
		}
	}
	return 0
}
