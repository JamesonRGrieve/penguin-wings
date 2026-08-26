// SPDX-License-Identifier: AGPL-3.0-or-later

package lxc

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pelican/wings/agent"
)

const agentTestToken = "tok-123"

func newAgentPair(t *testing.T) (*AgentClient, *agent.Supervisor) {
	t.Helper()
	buf := agent.NewLineBuffer(1000)
	sup := agent.NewSupervisor(buf)
	ts := httptest.NewServer(agent.NewServer(sup, buf, agentTestToken).Handler())
	t.Cleanup(ts.Close)
	t.Cleanup(func() { _ = sup.Stop() })
	return NewAgentClient(ts.URL, agentTestToken), sup
}

func TestAgentClientLifecycle(t *testing.T) {
	t.Parallel()
	c, sup := newAgentPair(t)
	ctx := context.Background()

	if err := c.StartProcess(ctx, "echo client-hello; exit 0", "", nil); err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	select {
	case <-sup.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("process did not exit")
	}

	lines, err := c.Readlog(ctx, 10)
	if err != nil {
		t.Fatalf("Readlog: %v", err)
	}
	found := false
	for _, l := range lines {
		if l == "client-hello" {
			found = true
		}
	}
	if !found {
		t.Errorf("Readlog = %v, want client-hello", lines)
	}

	code, oom, err := c.ExitState(ctx)
	if err != nil {
		t.Fatalf("ExitState: %v", err)
	}
	if code != 0 || oom {
		t.Errorf("ExitState = %d/%v, want 0/false", code, oom)
	}
}

func TestAgentClientBadToken(t *testing.T) {
	t.Parallel()
	buf := agent.NewLineBuffer(10)
	sup := agent.NewSupervisor(buf)
	ts := httptest.NewServer(agent.NewServer(sup, buf, "real-token").Handler())
	t.Cleanup(ts.Close)

	c := NewAgentClient(ts.URL, "wrong-token")
	if _, err := c.Readlog(context.Background(), 0); err == nil {
		t.Errorf("expected auth error with wrong token")
	}
}

func TestAgentClientAttachAndSendCommand(t *testing.T) {
	t.Parallel()
	c, _ := newAgentPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := c.StartProcess(ctx, "cat", "", nil); err != nil {
		t.Fatalf("StartProcess: %v", err)
	}

	lines := make(chan string, 16)
	if err := c.Attach(ctx, func(b []byte) { lines <- string(b) }); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	time.Sleep(100 * time.Millisecond) // let the console subscriber register

	if err := c.SendCommand(ctx, "hello-console"); err != nil {
		t.Fatalf("SendCommand: %v", err)
	}
	select {
	case got := <-lines:
		if got != "hello-console" {
			t.Errorf("attach line = %q, want hello-console", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no console line received via attach")
	}
}
