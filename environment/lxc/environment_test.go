// SPDX-License-Identifier: AGPL-3.0-or-later

package lxc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pelican/wings/environment"
	"github.com/pelican/wings/events"
	"github.com/pelican/wings/system"
)

func testEnv(t *testing.T, power *PVEClient) *Environment {
	t.Helper()
	return &Environment{
		id:      "srv-1",
		emitter: events.NewBus(),
		st:      system.NewAtomicString(environment.ProcessOfflineState),
		node:    "n1",
		vmid:    1,
		runner:  &Runner{},
		power:   power,
	}
}

func TestNewEnvironmentValidation(t *testing.T) {
	t.Parallel()
	valid := Config{ID: "s", Node: "n1", Runner: &Runner{}, Power: &PVEClient{}}
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"missing id", func(c *Config) { c.ID = "" }},
		{"missing node", func(c *Config) { c.Node = "" }},
		{"missing runner", func(c *Config) { c.Runner = nil }},
		{"missing power", func(c *Config) { c.Power = nil }},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := valid
			tc.mutate(&c)
			if _, err := New(c); err == nil {
				t.Errorf("want error for %s", tc.name)
			}
		})
	}
	if _, err := New(valid); err != nil {
		t.Errorf("valid config: unexpected error %v", err)
	}
}

func TestEnvironmentType(t *testing.T) {
	t.Parallel()
	if got := testEnv(t, &PVEClient{}).Type(); got != EnvironmentType {
		t.Errorf("Type() = %q, want %q", got, EnvironmentType)
	}
}

func TestEnvironmentStateMachine(t *testing.T) {
	t.Parallel()
	e := testEnv(t, &PVEClient{})
	if e.State() != environment.ProcessOfflineState {
		t.Fatalf("initial state = %q, want offline", e.State())
	}
	for _, s := range []string{
		environment.ProcessStartingState,
		environment.ProcessRunningState,
		environment.ProcessStoppingState,
		environment.ProcessOfflineState,
	} {
		e.SetState(s)
		if e.State() != s {
			t.Errorf("after SetState(%q), State() = %q", s, e.State())
		}
	}
}

func TestEnvironmentSetStateInvalidPanics(t *testing.T) {
	t.Parallel()
	e := testEnv(t, &PVEClient{})
	defer func() {
		if recover() == nil {
			t.Errorf("expected panic on invalid state")
		}
	}()
	e.SetState("bogus")
}

func TestEnvironmentAgentStubs(t *testing.T) {
	t.Parallel()
	e := testEnv(t, &PVEClient{})
	if err := e.Attach(context.Background()); !errors.Is(err, ErrAgentUnavailable) {
		t.Errorf("Attach err = %v, want ErrAgentUnavailable", err)
	}
	if err := e.SendCommand("say hi"); !errors.Is(err, ErrAgentUnavailable) {
		t.Errorf("SendCommand err = %v, want ErrAgentUnavailable", err)
	}
	if _, err := e.Readlog(10); !errors.Is(err, ErrAgentUnavailable) {
		t.Errorf("Readlog err = %v, want ErrAgentUnavailable", err)
	}
	if _, _, err := e.ExitState(); !errors.Is(err, ErrAgentUnavailable) {
		t.Errorf("ExitState err = %v, want ErrAgentUnavailable", err)
	}
}

func TestEnvironmentPowerReads(t *testing.T) {
	t.Parallel()

	t.Run("running", func(t *testing.T) {
		t.Parallel()
		e := testEnv(t, newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"data":{"status":"running","uptime":60,"vmid":1}}`)
		}))
		if running, err := e.IsRunning(context.Background()); err != nil || !running {
			t.Errorf("IsRunning = %v, %v; want true, nil", running, err)
		}
		if exists, err := e.Exists(); err != nil || !exists {
			t.Errorf("Exists = %v, %v; want true, nil", exists, err)
		}
		if up, err := e.Uptime(context.Background()); err != nil || up != 60000 {
			t.Errorf("Uptime = %d, %v; want 60000, nil", up, err)
		}
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		e := testEnv(t, newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, "Configuration file 'nodes/n1/lxc/1.conf' does not exist")
		}))
		if exists, err := e.Exists(); err != nil || exists {
			t.Errorf("Exists = %v, %v; want false, nil", exists, err)
		}
		if running, err := e.IsRunning(context.Background()); err != nil || running {
			t.Errorf("IsRunning = %v, %v; want false, nil", running, err)
		}
	})
}

func TestEnvironmentStart(t *testing.T) {
	t.Parallel()
	var statusCalls atomic.Int32
	e := testEnv(t, newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/status/start") {
			fmt.Fprint(w, `{"data":"UPID:start"}`)
			return
		}
		// status/current: first call (IsRunning) stopped, then running.
		status := StatusStopped
		if statusCalls.Add(1) >= 2 {
			status = StatusRunning
		}
		fmt.Fprintf(w, `{"data":{"status":%q,"vmid":1}}`, status)
	}))
	if err := e.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if e.State() != environment.ProcessRunningState {
		t.Errorf("state after Start = %q, want running", e.State())
	}
}
