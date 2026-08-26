// SPDX-License-Identifier: AGPL-3.0-or-later

package lxc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/pelican/wings/agent"
)

const defaultAgentTimeout = 15 * time.Second

// AgentClient talks to the in-container penguin-agent over its authenticated
// HTTP + websocket API. It is the runtime realization of the LXC environment's
// process-I/O methods (console, stdin, logs, exit).
type AgentClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewAgentClient returns a client for the agent reachable at baseURL
// (e.g. http://10.0.0.5:8443) authenticating with token.
func NewAgentClient(baseURL, token string) *AgentClient {
	return &AgentClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: defaultAgentTimeout},
	}
}

// StartProcess asks the agent to launch the game process.
func (c *AgentClient) StartProcess(ctx context.Context, command, cwd string, env []string) error {
	return c.postJSON(ctx, agent.PathStart, agent.StartRequest{Command: command, Cwd: cwd, Env: env}, nil)
}

// SendCommand writes a console command (a line) to the process stdin.
func (c *AgentClient) SendCommand(ctx context.Context, command string) error {
	return c.postJSON(ctx, agent.PathStdin, agent.StdinRequest{Data: command + "\n"}, nil)
}

// Readlog returns up to the last n retained output lines.
func (c *AgentClient) Readlog(ctx context.Context, n int) ([]string, error) {
	path := agent.PathLogs
	if n > 0 {
		path += "?" + agent.QueryLines + "=" + strconv.Itoa(n)
	}
	var out agent.LogsResponse
	if err := c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out.Lines, nil
}

// ExitState returns the game process exit code and OOM flag.
func (c *AgentClient) ExitState(ctx context.Context) (uint32, bool, error) {
	var out agent.ExitResponse
	if err := c.get(ctx, agent.PathExit, &out); err != nil {
		return 0, false, err
	}
	if out.Code < 0 {
		return 0, out.OOM, nil
	}
	return uint32(out.Code), out.OOM, nil
}

// Stats returns the agent's basic resource snapshot.
func (c *AgentClient) Stats(ctx context.Context) (agent.StatsResponse, error) {
	var out agent.StatsResponse
	err := c.get(ctx, agent.PathStats, &out)
	return out, err
}

// Attach opens the console websocket and streams each output line to onLine
// until the context is cancelled or the connection closes. It returns once the
// stream is established.
func (c *AgentClient) Attach(ctx context.Context, onLine func([]byte)) error {
	wsURL := "ws" + strings.TrimPrefix(c.baseURL, "http") + agent.PathConsole +
		"?" + agent.QueryToken + "=" + url.QueryEscape(c.token)
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("agent attach: %w", err)
	}
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	go func() {
		defer conn.Close()
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if onLine != nil {
				onLine(msg)
			}
		}
	}()
	return nil
}

func (c *AgentClient) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("agent request: %w", err)
	}
	return c.do(req, out)
}

func (c *AgentClient) postJSON(ctx context.Context, path string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("agent encode: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("agent request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *AgentClient) do(req *http.Request, out any) error {
	req.Header.Set(agent.HeaderAuth, agent.AuthScheme+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("agent %s %s: status %d", req.Method, req.URL.Path, resp.StatusCode)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("agent decode %s: %w", req.URL.Path, err)
		}
	}
	return nil
}
