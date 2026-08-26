// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

// Wire protocol shared by the penguin-agent server and the Wings client.
const (
	// HeaderAuth carries the bearer token; websocket clients may instead pass it
	// as a "token" query parameter.
	HeaderAuth  = "Authorization"
	AuthScheme  = "Bearer "
	QueryToken  = "token"
	QueryLines  = "lines"
	consoleNewl = "\n"

	PathStart   = "/v1/start"
	PathStop    = "/v1/stop"
	PathSignal  = "/v1/signal"
	PathStdin   = "/v1/stdin"
	PathLogs    = "/v1/logs"
	PathExit    = "/v1/exit"
	PathStats   = "/v1/stats"
	PathConsole = "/v1/console"
)

// StartRequest launches the game process. Command is a shell invocation run via
// `sh -c`, matching the Panel-provided startup command.
type StartRequest struct {
	Command string   `json:"command"`
	Cwd     string   `json:"cwd,omitempty"`
	Env     []string `json:"env,omitempty"`
}

// SignalRequest sends a named signal (TERM, KILL, INT, HUP, QUIT).
type SignalRequest struct {
	Signal string `json:"signal"`
}

// StdinRequest writes data to the process stdin.
type StdinRequest struct {
	Data string `json:"data"`
}

// LogsResponse returns the most recent retained output lines.
type LogsResponse struct {
	Lines []string `json:"lines"`
}

// ExitResponse reports the process exit state.
type ExitResponse struct {
	Exited bool `json:"exited"`
	Code   int  `json:"code"`
	OOM    bool `json:"oom"`
}

// StatsResponse reports basic resource usage of the supervised process.
type StatsResponse struct {
	Running     bool  `json:"running"`
	MemoryBytes int64 `json:"memory_bytes"`
}
