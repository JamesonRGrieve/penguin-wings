// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

func waitDone(t *testing.T, s *Supervisor, d time.Duration) {
	t.Helper()
	select {
	case <-s.Done():
	case <-time.After(d):
		t.Fatal("process did not exit in time")
	}
}

func hasLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

func TestSupervisorRunAndExit(t *testing.T) {
	t.Parallel()
	buf := NewLineBuffer(100)
	s := NewSupervisor(buf)
	if err := s.Start(context.Background(), "sh", []string{"-c", "echo hello; echo world 1>&2; exit 0"}, "", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitDone(t, s, 5*time.Second)

	exited, code, oom := s.Exit()
	if !exited || code != 0 || oom {
		t.Errorf("Exit = %v/%d/%v, want true/0/false", exited, code, oom)
	}
	lines := buf.Lines(0)
	if !hasLine(lines, "hello") || !hasLine(lines, "world") {
		t.Errorf("captured lines = %v, want stdout+stderr merged (hello, world)", lines)
	}
}

func TestSupervisorExitCode(t *testing.T) {
	t.Parallel()
	s := NewSupervisor(NewLineBuffer(10))
	if err := s.Start(context.Background(), "sh", []string{"-c", "exit 3"}, "", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitDone(t, s, 5*time.Second)
	if _, code, _ := s.Exit(); code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
}

func TestSupervisorStdinAndStop(t *testing.T) {
	t.Parallel()
	buf := NewLineBuffer(100)
	s := NewSupervisor(buf)
	if err := s.Start(context.Background(), "cat", nil, "", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.WriteStdin("ping\n"); err != nil {
		t.Fatalf("WriteStdin: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !hasLine(buf.Lines(0), "ping") {
		time.Sleep(20 * time.Millisecond)
	}
	if !hasLine(buf.Lines(0), "ping") {
		t.Errorf("stdin echo not captured: %v", buf.Lines(0))
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitDone(t, s, 5*time.Second)
	if s.Running() {
		t.Errorf("supervisor still running after Stop")
	}
}

func TestSupervisorAlreadyRunning(t *testing.T) {
	t.Parallel()
	s := NewSupervisor(NewLineBuffer(10))
	if err := s.Start(context.Background(), "sleep", []string{"5"}, "", nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Stop()
		waitDone(t, s, 5*time.Second)
	})
	if err := s.Start(context.Background(), "sleep", []string{"1"}, "", nil); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("second Start = %v, want ErrAlreadyRunning", err)
	}
}
