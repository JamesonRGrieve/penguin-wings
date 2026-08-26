// SPDX-License-Identifier: AGPL-3.0-or-later

package lxc

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunnerEnv(t *testing.T) {
	t.Setenv("PENGUIN_TEST_MARKER", "present")
	t.Setenv("TF_LOG", "DEBUG") // tfexec-managed; must be filtered out

	env := runnerEnv("user@pve!t=secret-uuid")

	if got := env[EnvAPIToken]; got != "user@pve!t=secret-uuid" {
		t.Errorf("%s = %q, want the injected token", EnvAPIToken, got)
	}
	if got := env["PENGUIN_TEST_MARKER"]; got != "present" {
		t.Errorf("inherited env var lost: PENGUIN_TEST_MARKER = %q", got)
	}
	if _, ok := env["TF_LOG"]; ok {
		t.Errorf("TF_* vars must be filtered so tfexec can manage them")
	}
}

func TestNewRunnerValidation(t *testing.T) {
	t.Parallel()

	if _, err := NewRunner(RunnerConfig{WorkDir: t.TempDir()}); !errors.Is(err, ErrTofuBinary) {
		t.Errorf("empty ExecPath: want ErrTofuBinary, got %v", err)
	}
	if _, err := NewRunner(RunnerConfig{ExecPath: "/opt/tofu"}); !errors.Is(err, ErrWorkDir) {
		t.Errorf("empty WorkDir: want ErrWorkDir, got %v", err)
	}
}

func TestRunnerWriteConfig(t *testing.T) {
	t.Parallel()

	execPath, err := LookupExecPath()
	if err != nil {
		t.Skipf("no tofu/terraform binary available: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "srv-1")
	r, err := NewRunner(RunnerConfig{ExecPath: execPath, WorkDir: dir, APIToken: "user@pve!t=secret-uuid"})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("work dir not created: %v", err)
	}
	if err := r.WriteConfig(validSpec(), validProvider()); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ConfigFileName))
	if err != nil {
		t.Fatalf("read written config: %v", err)
	}
	if !json.Valid(data) {
		t.Fatalf("written config is not valid JSON:\n%s", data)
	}
	want, err := Render(validSpec(), validProvider())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if string(data) != string(want) {
		t.Errorf("written config differs from Render output")
	}
	// The credential must not reach disk.
	if strings.Contains(string(data), "secret-uuid") {
		t.Errorf("API token leaked into the written config")
	}
}

// TestRunnerInitPlanIntegration exercises the embedded-Tofu path against a real
// Proxmox node: WriteConfig -> Init -> Plan (read-only; no apply). It is skipped
// unless the operator supplies a live target, keeping the default suite hermetic.
func TestRunnerInitPlanIntegration(t *testing.T) {
	endpoint := os.Getenv("TEST_PVE_ENDPOINT")
	token := os.Getenv("TEST_PVE_API_TOKEN")
	node := os.Getenv("TEST_PVE_NODE")
	template := os.Getenv("TEST_PVE_TEMPLATE")
	if endpoint == "" || token == "" || node == "" || template == "" {
		t.Skip("set TEST_PVE_ENDPOINT, TEST_PVE_API_TOKEN, TEST_PVE_NODE, TEST_PVE_TEMPLATE to run")
	}
	execPath, err := LookupExecPath()
	if err != nil {
		t.Skipf("no tofu/terraform binary available: %v", err)
	}

	dir := t.TempDir()
	r, err := NewRunner(RunnerConfig{ExecPath: execPath, WorkDir: dir, APIToken: token})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	spec := validSpec()
	spec.Node = node
	spec.TemplateFileID = template
	spec.IPv4 = IPv4Config{Address: DHCPAddress}
	provider := ProviderConfig{Endpoint: endpoint, Insecure: os.Getenv("TEST_PVE_INSECURE") == "true"}

	if err := r.WriteConfig(spec, provider); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := r.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := r.Plan(ctx); err != nil {
		t.Fatalf("Plan: %v", err)
	}
}
