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
	createTimeout  = 15 * time.Minute
	destroyTimeout = 10 * time.Minute
	existsTimeout  = 30 * time.Second
)

// ErrConsolePending marks console operations (Attach/SendCommand) that will be
// served via PVE's terminal proxy, which is not yet wired.
var ErrConsolePending = errors.New("lxc: PVE console not yet wired")

// Environment realizes a Penguin server as a persistent Proxmox LXC. Infra
// lifecycle (create/destroy/exists) runs through embedded OpenTofu (Runner);
// power state and process visibility (start/stop/status/console/metrics) run
// through the PVE API. The game runs from the egg's own OCI image as the image's
// non-root user, so there is no in-container agent.
type Environment struct {
	mu sync.RWMutex

	// id is the server UUID.
	id string

	config  *environment.Configuration
	emitter *events.Bus
	st      *system.AtomicString

	logCallbackMx sync.Mutex
	logCallback   func([]byte)

	node         string
	vmid         int
	spec         LXCSpec
	provider     ProviderConfig
	imageStorage string

	runner *Runner
	power  *PVEClient
	ssh    *SSHClient
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
	// SSH is the scoped node channel for the install phase; nil disables install.
	SSH *SSHClient
	// ImageStorage is the vztmpl-capable storage egg OCI images are pulled onto;
	// defaults to DefaultImageStorage.
	ImageStorage string
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
	imageStorage := cfg.ImageStorage
	if imageStorage == "" {
		imageStorage = DefaultImageStorage
	}
	return &Environment{
		id:           cfg.ID,
		config:       cfg.Configuration,
		emitter:      events.NewBus(),
		st:           system.NewAtomicString(environment.ProcessOfflineState),
		node:         cfg.Node,
		vmid:         cfg.VMID,
		spec:         cfg.Spec,
		provider:     cfg.Provider,
		imageStorage: imageStorage,
		runner:       cfg.Runner,
		power:        cfg.Power,
		ssh:          cfg.SSH,
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

// SetLogCallback registers the sink for the container's console output (fed by
// the PVE console stream once attached).
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

// Create realizes the container via `tofu apply` (idempotent). When the spec
// carries an egg OCI image rather than a resolved template, the image is first
// pulled onto storage (idempotent, shared across servers on the same egg) and the
// resulting vztmpl volid becomes the container's template.
func (e *Environment) Create() error {
	ctx, cancel := context.WithTimeout(context.Background(), createTimeout)
	defer cancel()
	spec := e.spec
	if spec.TemplateFileID == "" && spec.Image != "" {
		volid, err := e.power.EnsureOCIImage(ctx, e.node, e.imageStorage, spec.Image)
		if err != nil {
			return fmt.Errorf("ensure egg image: %w", err)
		}
		spec.TemplateFileID = volid
	}
	if err := e.runner.WriteConfig(spec, e.provider); err != nil {
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

// Attach streams the container console to the registered log callback via PVE's
// terminal proxy until the context is cancelled.
// TODO: wire the PVE termproxy/vncwebsocket console protocol.
func (e *Environment) Attach(_ context.Context) error { return ErrConsolePending }

// SendCommand writes a console command to the container via PVE's terminal proxy.
// TODO: wire the PVE termproxy console.
func (e *Environment) SendCommand(string) error { return ErrConsolePending }

// Readlog returns recent console output. PVE keeps no historical console log —
// the live stream comes from Attach — so this returns nothing.
func (e *Environment) Readlog(int) ([]string, error) { return nil, nil }

// ExitState is best-effort: PVE does not expose the game process exit code, so a
// stopped container reports a clean (code 0, no OOM) exit. Crash handling keys
// off the stopped state instead.
func (e *Environment) ExitState() (uint32, bool, error) { return 0, false, nil }
