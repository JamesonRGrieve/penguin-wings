// SPDX-License-Identifier: AGPL-3.0-or-later

package lxc

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/pelican/wings/environment"
	"github.com/pelican/wings/events"
	"github.com/pelican/wings/system"
)

// Ensure the LXC environment always satisfies the base environment interface.
var _ environment.ProcessEnvironment = (*Environment)(nil)

// EnvironmentType is the Type() identifier for the LXC environment.
const EnvironmentType = "lxc"

// Internal timeouts for the interface methods that take no context of their own.
const (
	createTimeout    = 15 * time.Minute
	destroyTimeout   = 10 * time.Minute
	existsTimeout    = 30 * time.Second
	agentCallTimeout = 15 * time.Second
)

// ErrAgentUnavailable marks process-I/O operations (console, stdin, logs, exit
// state) that require the in-container penguin-agent, which is not yet built.
var ErrAgentUnavailable = errors.New("penguin-agent unavailable: process I/O not yet implemented")

// Environment realizes a Penguin server as a persistent Proxmox LXC. Infra
// lifecycle (create/destroy/exists) runs through embedded OpenTofu (Runner);
// power state (start/stop/status) runs through the PVE API (PVEClient); process
// I/O (console/stdin/logs/exit) will run through the in-container penguin-agent.
type Environment struct {
	mu sync.RWMutex

	// id is the server UUID.
	id string

	config  *environment.Configuration
	emitter *events.Bus
	st      *system.AtomicString

	logCallbackMx sync.Mutex
	logCallback   func([]byte)

	node     string
	vmid     int
	spec     LXCSpec
	provider ProviderConfig

	runner *Runner
	power  *PVEClient

	agentPort  int
	agentToken string
}

// Config bundles everything needed to build an LXC environment for one server.
type Config struct {
	ID            string
	Configuration *environment.Configuration
	Node          string
	VMID          int
	Spec          LXCSpec
	Provider      ProviderConfig
	Runner        *Runner
	Power         *PVEClient
	AgentPort     int
	AgentToken    string
}

// New builds an LXC environment. The container need not exist yet.
func New(cfg Config) (*Environment, error) {
	switch {
	case cfg.ID == "":
		return nil, fmt.Errorf("lxc environment: id is required")
	case cfg.Node == "":
		return nil, fmt.Errorf("lxc environment: node is required")
	case cfg.Runner == nil:
		return nil, fmt.Errorf("lxc environment: runner is required")
	case cfg.Power == nil:
		return nil, fmt.Errorf("lxc environment: power client is required")
	}
	return &Environment{
		id:         cfg.ID,
		config:     cfg.Configuration,
		emitter:    events.NewBus(),
		st:         system.NewAtomicString(environment.ProcessOfflineState),
		node:       cfg.Node,
		vmid:       cfg.VMID,
		spec:       cfg.Spec,
		provider:   cfg.Provider,
		runner:     cfg.Runner,
		power:      cfg.Power,
		agentPort:  cfg.AgentPort,
		agentToken: cfg.AgentToken,
	}, nil
}

// Type returns the environment identifier.
func (e *Environment) Type() string { return EnvironmentType }

// Config returns the environment configuration.
func (e *Environment) Config() *environment.Configuration {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.config
}

// Events returns the environment's event bus (subscribe-only for callers).
func (e *Environment) Events() *events.Bus { return e.emitter }

// State returns the tracked environment state.
func (e *Environment) State() string { return e.st.Load() }

// SetState updates the tracked state and, on a transition, publishes a change
// event for the server layer to react to.
func (e *Environment) SetState(state string) {
	switch state {
	case environment.ProcessOfflineState,
		environment.ProcessStartingState,
		environment.ProcessRunningState,
		environment.ProcessStoppingState:
	default:
		panic(fmt.Errorf("lxc environment: invalid state %q", state))
	}
	if e.State() != state {
		e.st.Store(state)
		e.Events().Publish(environment.StateChangeEvent, state)
	}
}

// SetLogCallback registers the sink for the container's log output (fed by the
// penguin-agent once attached).
func (e *Environment) SetLogCallback(f func([]byte)) {
	e.logCallbackMx.Lock()
	defer e.logCallbackMx.Unlock()
	e.logCallback = f
}

// Exists reports whether the container is present on the node.
func (e *Environment) Exists() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), existsTimeout)
	defer cancel()
	if _, err := e.power.Status(ctx, e.node, e.vmid); err != nil {
		if IsContainerNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// IsRunning reports whether the container is powered on.
func (e *Environment) IsRunning(ctx context.Context) (bool, error) {
	st, err := e.power.Status(ctx, e.node, e.vmid)
	if err != nil {
		if IsContainerNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return st.Running(), nil
}

// Create realizes the container via `tofu apply` (idempotent).
func (e *Environment) Create() error {
	ctx, cancel := context.WithTimeout(context.Background(), createTimeout)
	defer cancel()
	if err := e.runner.WriteConfig(e.spec, e.provider); err != nil {
		return err
	}
	if err := e.runner.Init(ctx); err != nil {
		return err
	}
	if err := e.runner.Apply(ctx); err != nil {
		return err
	}
	return nil
}

// Destroy removes the container via `tofu destroy` and marks the state offline.
func (e *Environment) Destroy() error {
	ctx, cancel := context.WithTimeout(context.Background(), destroyTimeout)
	defer cancel()
	if err := e.runner.Destroy(ctx); err != nil {
		return err
	}
	e.SetState(environment.ProcessOfflineState)
	return nil
}

// OnBeforeStart ensures the container exists before a start is attempted.
func (e *Environment) OnBeforeStart(ctx context.Context) error {
	ok, err := e.Exists()
	if err != nil {
		return err
	}
	if !ok {
		return e.Create()
	}
	return nil
}

// Start powers the container on and blocks until it reports running.
func (e *Environment) Start(ctx context.Context) error {
	running, err := e.IsRunning(ctx)
	if err != nil {
		return err
	}
	if running {
		e.SetState(environment.ProcessRunningState)
		return nil
	}
	e.SetState(environment.ProcessStartingState)
	if err := e.power.Start(ctx, e.node, e.vmid); err != nil {
		e.SetState(environment.ProcessOfflineState)
		return err
	}
	if err := e.power.WaitForStatus(ctx, e.node, e.vmid, StatusRunning); err != nil {
		return err
	}
	e.SetState(environment.ProcessRunningState)
	return nil
}

// Stop requests a graceful shutdown. A container that is already stopped is a
// no-op.
func (e *Environment) Stop(ctx context.Context) error {
	running, err := e.IsRunning(ctx)
	if err != nil {
		return err
	}
	if !running {
		e.SetState(environment.ProcessOfflineState)
		return nil
	}
	e.SetState(environment.ProcessStoppingState)
	return e.power.Shutdown(ctx, e.node, e.vmid, 0)
}

// WaitForStop waits for the container to stop within duration. If it is still
// running after the wait and terminate is true, it is force-stopped.
func (e *Environment) WaitForStop(ctx context.Context, duration time.Duration, terminate bool) error {
	wctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	if err := e.power.WaitForStatus(wctx, e.node, e.vmid, StatusStopped); err != nil {
		if terminate {
			return e.Terminate(ctx, "")
		}
		return err
	}
	e.SetState(environment.ProcessOfflineState)
	return nil
}

// Terminate force-stops the container. The signal is ignored — a PVE stop is a
// hard power-off. A container that is already stopped is a no-op.
func (e *Environment) Terminate(ctx context.Context, _ string) error {
	running, err := e.IsRunning(ctx)
	if err != nil {
		return err
	}
	if !running {
		e.SetState(environment.ProcessOfflineState)
		return nil
	}
	e.SetState(environment.ProcessStoppingState)
	if err := e.power.Stop(ctx, e.node, e.vmid); err != nil {
		return err
	}
	e.SetState(environment.ProcessOfflineState)
	return nil
}

// InSituUpdate would push changed resource limits without a restart. Live LXC
// resource changes are a targeted converge that is not yet wired, so this is a
// no-op for now (the interface permits it).
func (e *Environment) InSituUpdate() error { return nil }

// Uptime returns the container uptime in milliseconds, or 0 when stopped.
func (e *Environment) Uptime(ctx context.Context) (int64, error) {
	st, err := e.power.Status(ctx, e.node, e.vmid)
	if err != nil {
		if IsContainerNotFound(err) {
			return 0, nil
		}
		return 0, err
	}
	return st.Uptime * int64(time.Second/time.Millisecond), nil
}

// agentClient resolves the in-container agent's address (via the container's
// live IP) and returns a client for it. Returns ErrAgentUnavailable when no
// agent token is configured.
func (e *Environment) agentClient(ctx context.Context) (*AgentClient, error) {
	if e.agentToken == "" {
		return nil, ErrAgentUnavailable
	}
	ip, err := e.power.ContainerIPv4(ctx, e.node, e.vmid)
	if err != nil {
		return nil, fmt.Errorf("resolve agent address: %w", err)
	}
	return NewAgentClient(fmt.Sprintf("http://%s:%d", ip, e.agentPort), e.agentToken), nil
}

// Attach streams the container console output to the registered log callback via
// the penguin-agent until the context is cancelled.
func (e *Environment) Attach(ctx context.Context) error {
	client, err := e.agentClient(ctx)
	if err != nil {
		return err
	}
	return client.Attach(ctx, func(line []byte) {
		e.logCallbackMx.Lock()
		cb := e.logCallback
		e.logCallbackMx.Unlock()
		if cb != nil {
			cb(line)
		}
	})
}

// SendCommand writes a console command to the game process stdin via the agent.
func (e *Environment) SendCommand(command string) error {
	ctx, cancel := context.WithTimeout(context.Background(), agentCallTimeout)
	defer cancel()
	client, err := e.agentClient(ctx)
	if err != nil {
		return err
	}
	return client.SendCommand(ctx, command)
}

// Readlog returns up to the last n console lines via the agent.
func (e *Environment) Readlog(n int) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), agentCallTimeout)
	defer cancel()
	client, err := e.agentClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.Readlog(ctx, n)
}

// ExitState returns the game process exit code and OOM flag via the agent.
func (e *Environment) ExitState() (uint32, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), agentCallTimeout)
	defer cancel()
	client, err := e.agentClient(ctx)
	if err != nil {
		return 0, false, err
	}
	return client.ExitState(ctx)
}
