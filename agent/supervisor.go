// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

const outputScanMax = 1024 * 1024 // 1 MiB per line ceiling

var (
	// ErrAlreadyRunning is returned when Start is called while a process runs.
	ErrAlreadyRunning = errors.New("agent: process already running")
	// ErrNotRunning is returned for stdin/signal operations with no live process.
	ErrNotRunning = errors.New("agent: process not running")
)

// Supervisor runs and monitors a single child process (the game server),
// merging its stdout+stderr into a LineBuffer and tracking its exit state.
type Supervisor struct {
	mu       sync.Mutex
	buf      *LineBuffer
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	running  bool
	exited   bool
	exitCode int
	oom      bool
	done     chan struct{}
}

// NewSupervisor returns a supervisor that writes process output to buf.
func NewSupervisor(buf *LineBuffer) *Supervisor {
	return &Supervisor{buf: buf, done: make(chan struct{})}
}

// Start launches the process. Its merged output is streamed into the buffer and
// its exit state recorded when it terminates.
func (s *Supervisor) Start(ctx context.Context, name string, args []string, dir string, env []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return ErrAlreadyRunning
	}

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("agent: stdin pipe: %w", err)
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("agent: output pipe: %w", err)
	}
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		return fmt.Errorf("agent: start process: %w", err)
	}
	pw.Close() // the child holds its own copy of the write end

	s.cmd = cmd
	s.stdin = stdin
	s.running = true
	s.exited = false
	s.exitCode = 0
	s.oom = false
	s.done = make(chan struct{})
	done := s.done

	go s.readOutput(pr)
	go s.wait(cmd, done)
	return nil
}

func (s *Supervisor) readOutput(pr *os.File) {
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 0, 64*1024), outputScanMax)
	for sc.Scan() {
		s.buf.Append(sc.Text())
	}
	pr.Close()
}

func (s *Supervisor) wait(cmd *exec.Cmd, done chan struct{}) {
	waitErr := cmd.Wait()
	s.mu.Lock()
	s.running = false
	s.exited = true
	s.exitCode, s.oom = exitInfo(waitErr)
	s.mu.Unlock()
	close(done)
}

// exitInfo derives the exit code and an approximate OOM flag from the wait
// error. A process killed by SIGKILL in a memory-limited container is treated as
// OOM (an approximation; a precise reading would consult cgroup memory.events).
func exitInfo(waitErr error) (code int, oom bool) {
	if waitErr == nil {
		return 0, false
	}
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) {
		code = ee.ExitCode()
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			sig := ws.Signal()
			code = 128 + int(sig)
			if sig == syscall.SIGKILL {
				oom = true
			}
		}
		return code, oom
	}
	return 1, false
}

// WriteStdin writes data to the process stdin.
func (s *Supervisor) WriteStdin(data string) error {
	s.mu.Lock()
	stdin, running := s.stdin, s.running
	s.mu.Unlock()
	if !running || stdin == nil {
		return ErrNotRunning
	}
	_, err := io.WriteString(stdin, data)
	return err
}

// Signal sends sig to the process.
func (s *Supervisor) Signal(sig os.Signal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || s.cmd == nil || s.cmd.Process == nil {
		return ErrNotRunning
	}
	return s.cmd.Process.Signal(sig)
}

// Stop requests a graceful termination (SIGTERM).
func (s *Supervisor) Stop() error { return s.Signal(syscall.SIGTERM) }

// Running reports whether a process is currently running.
func (s *Supervisor) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Exit returns whether the process has exited, its exit code, and the OOM flag.
func (s *Supervisor) Exit() (exited bool, code int, oom bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exited, s.exitCode, s.oom
}

// Done returns a channel closed when the current process exits.
func (s *Supervisor) Done() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}
