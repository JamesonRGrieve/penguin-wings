// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const srvToken = "sekret-token"

func newTestServer(t *testing.T) (*httptest.Server, *Supervisor, *LineBuffer) {
	t.Helper()
	buf := NewLineBuffer(1000)
	sup := NewSupervisor(buf)
	ts := httptest.NewServer(NewServer(sup, buf, srvToken).Handler())
	t.Cleanup(ts.Close)
	return ts, sup, buf
}

func doReq(t *testing.T, method, url, body, token string) *http.Response {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set(HeaderAuth, "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestServerAuth(t *testing.T) {
	t.Parallel()
	ts, _, _ := newTestServer(t)
	for _, tc := range []struct {
		name  string
		token string
		want  int
	}{
		{"no token", "", http.StatusUnauthorized},
		{"wrong token", "nope", http.StatusUnauthorized},
		{"correct token", srvToken, http.StatusOK},
	} {
		resp := doReq(t, http.MethodGet, ts.URL+PathLogs, "", tc.token)
		if resp.StatusCode != tc.want {
			t.Errorf("%s: status %d, want %d", tc.name, resp.StatusCode, tc.want)
		}
		resp.Body.Close()
	}
}

func TestServerStartLogsExit(t *testing.T) {
	t.Parallel()
	ts, sup, _ := newTestServer(t)

	resp := doReq(t, http.MethodPost, ts.URL+PathStart, `{"command":"echo agent-hello; exit 0"}`, srvToken)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("start: status %d", resp.StatusCode)
	}
	resp.Body.Close()
	waitDone(t, sup, 5*time.Second)

	resp = doReq(t, http.MethodGet, ts.URL+PathLogs+"?lines=10", "", srvToken)
	var logs LogsResponse
	_ = json.NewDecoder(resp.Body).Decode(&logs)
	resp.Body.Close()
	if !hasLine(logs.Lines, "agent-hello") {
		t.Errorf("logs = %v, want agent-hello", logs.Lines)
	}

	resp = doReq(t, http.MethodGet, ts.URL+PathExit, "", srvToken)
	var ex ExitResponse
	_ = json.NewDecoder(resp.Body).Decode(&ex)
	resp.Body.Close()
	if !ex.Exited || ex.Code != 0 || ex.OOM {
		t.Errorf("exit = %+v, want exited/0/false", ex)
	}
}

func TestServerConsoleWebsocket(t *testing.T) {
	t.Parallel()
	ts, _, buf := newTestServer(t)
	buf.Append("backlog-line")

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + PathConsole + "?token=" + srvToken
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, msg, err := conn.ReadMessage(); err != nil || string(msg) != "backlog-line" {
		t.Fatalf("backlog read = %q, %v; want backlog-line", string(msg), err)
	}

	// Give the handler a moment to register the live subscriber, then append.
	time.Sleep(100 * time.Millisecond)
	buf.Append("live-line")
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, msg, err := conn.ReadMessage(); err != nil || string(msg) != "live-line" {
		t.Fatalf("live read = %q, %v; want live-line", string(msg), err)
	}
}
