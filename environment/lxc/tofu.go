// SPDX-License-Identifier: AGPL-3.0-or-later

package lxc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hashicorp/terraform-exec/tfexec"
)

// EnvAPIToken is the environment variable the bpg/proxmox provider reads its API
// token from. The runner sets it on the tofu child process only; it is never
// written into the workspace, passed as an argument, or logged.
const EnvAPIToken = "PROXMOX_VE_API_TOKEN"

// workDirPerm / configPerm keep the per-server workspace and its generated config
// tight, since Tofu state can hold sensitive-adjacent data.
const (
	workDirPerm os.FileMode = 0o700
	configPerm  os.FileMode = 0o600
)

// ErrTofuBinary indicates the OpenTofu/Terraform binary path is missing.
var ErrTofuBinary = errors.New("tofu binary not configured")

// ErrWorkDir indicates the runner's workspace directory is missing.
var ErrWorkDir = errors.New("runner work dir is required")

// Runner drives OpenTofu (via terraform-exec) over a single server's workspace.
// One Runner corresponds to exactly one server and one Tofu state.
type Runner struct {
	tf      *tfexec.Terraform
	workDir string
}

// RunnerConfig configures a Runner.
type RunnerConfig struct {
	// ExecPath is the absolute path to the tofu (or terraform) binary.
	ExecPath string
	// WorkDir is the server's dedicated workspace directory, created if absent.
	// One server per directory.
	WorkDir string
	// APIToken is the bpg provider credential (user@realm!tokenid=secret). It is
	// handed to the tofu child process via EnvAPIToken and never persisted or
	// logged.
	APIToken string
}

// NewRunner prepares a per-server Tofu runner. The workspace directory is created
// if it does not exist and the provider credential is bound to the tofu process
// environment.
func NewRunner(cfg RunnerConfig) (*Runner, error) {
	if strings.TrimSpace(cfg.ExecPath) == "" {
		return nil, ErrTofuBinary
	}
	if strings.TrimSpace(cfg.WorkDir) == "" {
		return nil, ErrWorkDir
	}
	if err := os.MkdirAll(cfg.WorkDir, workDirPerm); err != nil {
		return nil, fmt.Errorf("create work dir: %w", err)
	}
	tf, err := tfexec.NewTerraform(cfg.WorkDir, cfg.ExecPath)
	if err != nil {
		return nil, fmt.Errorf("init terraform-exec: %w", err)
	}
	if err := tf.SetEnv(runnerEnv(cfg.APIToken)); err != nil {
		return nil, fmt.Errorf("set tofu env: %w", err)
	}
	return &Runner{tf: tf, workDir: cfg.WorkDir}, nil
}

// runnerEnv builds the child-process environment: the parent's env minus any TF_*
// variables (which terraform-exec manages itself and rejects in SetEnv), plus the
// provider API token.
func runnerEnv(token string) map[string]string {
	env := make(map[string]string)
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || strings.HasPrefix(k, "TF_") {
			continue
		}
		env[k] = v
	}
	env[EnvAPIToken] = token
	return env
}

// WriteConfig renders spec+provider and writes ConfigFileName into the workspace,
// replacing any prior config. Call before Init/Plan/Apply.
func (r *Runner) WriteConfig(spec LXCSpec, provider ProviderConfig) error {
	data, err := Render(spec, provider)
	if err != nil {
		return err
	}
	path := filepath.Join(r.workDir, ConfigFileName)
	if err := os.WriteFile(path, data, configPerm); err != nil {
		return fmt.Errorf("write %s: %w", ConfigFileName, err)
	}
	return nil
}

// Init runs `tofu init`, installing the pinned provider into the workspace.
func (r *Runner) Init(ctx context.Context) error {
	if err := r.tf.Init(ctx, tfexec.Upgrade(false)); err != nil {
		return fmt.Errorf("tofu init: %w", err)
	}
	return nil
}

// Plan runs `tofu plan` and reports whether a non-empty diff exists.
func (r *Runner) Plan(ctx context.Context) (bool, error) {
	changed, err := r.tf.Plan(ctx)
	if err != nil {
		return false, fmt.Errorf("tofu plan: %w", err)
	}
	return changed, nil
}

// Apply runs `tofu apply`, realizing the container. This mutates the target
// Proxmox node and must only run under the change-safety discipline.
func (r *Runner) Apply(ctx context.Context) error {
	if err := r.tf.Apply(ctx); err != nil {
		return fmt.Errorf("tofu apply: %w", err)
	}
	return nil
}

// Destroy runs `tofu destroy`, removing the container on server deletion.
func (r *Runner) Destroy(ctx context.Context) error {
	if err := r.tf.Destroy(ctx); err != nil {
		return fmt.Errorf("tofu destroy: %w", err)
	}
	return nil
}

// LookupExecPath resolves the OpenTofu (preferred) or Terraform binary on PATH.
func LookupExecPath() (string, error) {
	for _, name := range []string{"tofu", "terraform"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("%w: no tofu or terraform found on PATH", ErrTofuBinary)
}
