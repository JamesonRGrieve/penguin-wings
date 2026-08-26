// SPDX-License-Identifier: AGPL-3.0-or-later

package lxc

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
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
	if _, ok := runnerEnv("")[EnvAPIToken]; ok {
		t.Errorf("empty token must leave %s unset so inherited/password auth is preserved", EnvAPIToken)
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
	// Auth is either an API token (TEST_PVE_API_TOKEN) or username/password taken
	// from the inherited PROXMOX_VE_USERNAME/PROXMOX_VE_PASSWORD environment.
	hasUserPass := os.Getenv("PROXMOX_VE_USERNAME") != "" && os.Getenv("PROXMOX_VE_PASSWORD") != ""
	if endpoint == "" || node == "" || template == "" || (token == "" && !hasUserPass) {
		t.Skip("set TEST_PVE_ENDPOINT/_NODE/_TEMPLATE and either TEST_PVE_API_TOKEN or PROXMOX_VE_USERNAME+PROXMOX_VE_PASSWORD to run")
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

// TestRunnerApplyDestroyIntegration is the live create/destroy proof: it applies
// one throwaway stopped LXC, asserts an immediate re-plan is a no-op (proving the
// container exists and matches intent), and always destroys it via t.Cleanup so
// it can never leak. Double-guarded — it only runs when TEST_PVE_APPLY=1 AND the
// target env is set — so the default suite never mutates a hypervisor.
func TestRunnerApplyDestroyIntegration(t *testing.T) {
	if os.Getenv("TEST_PVE_APPLY") != "1" {
		t.Skip("set TEST_PVE_APPLY=1 (plus the TEST_PVE_* target vars) to run the live create/destroy proof")
	}
	endpoint := os.Getenv("TEST_PVE_ENDPOINT")
	token := os.Getenv("TEST_PVE_API_TOKEN")
	node := os.Getenv("TEST_PVE_NODE")
	template := os.Getenv("TEST_PVE_TEMPLATE")
	storage := os.Getenv("TEST_PVE_STORAGE")
	bridge := os.Getenv("TEST_PVE_BRIDGE")
	hasUserPass := os.Getenv("PROXMOX_VE_USERNAME") != "" && os.Getenv("PROXMOX_VE_PASSWORD") != ""
	if endpoint == "" || node == "" || template == "" || storage == "" || bridge == "" || (token == "" && !hasUserPass) {
		t.Skip("set TEST_PVE_ENDPOINT/_NODE/_TEMPLATE/_STORAGE/_BRIDGE and either TEST_PVE_API_TOKEN or PROXMOX_VE_USERNAME+PROXMOX_VE_PASSWORD")
	}

	vmid := 0
	if v := os.Getenv("TEST_PVE_VMID"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("bad TEST_PVE_VMID %q: %v", v, err)
		}
		vmid = n
	}
	osType := os.Getenv("TEST_PVE_OSTYPE")
	if osType == "" {
		osType = DefaultOSType
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

	spec := LXCSpec{
		Node:           node,
		VMID:           vmid,
		Hostname:       "penguin-spike",
		Description:    "Penguin Wings lifecycle proof — safe to delete",
		Tags:           []string{"penguin", "spike"},
		TemplateFileID: template,
		OSType:         osType,
		Unprivileged:   true,
		Cores:          1,
		MemoryMiB:      512,
		RootDatastore:  storage,
		RootSizeGiB:    2,
		NetworkName:    DefaultNetworkName,
		Bridge:         bridge,
		IPv4:           IPv4Config{Address: DHCPAddress},
	}
	provider := ProviderConfig{Endpoint: endpoint, Insecure: os.Getenv("TEST_PVE_INSECURE") == "true"}

	if err := r.WriteConfig(spec, provider); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if err := r.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Register destroy before apply so a failure after create still tears down.
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer ccancel()
		if err := r.Destroy(cctx); err != nil {
			t.Errorf("cleanup Destroy: %v", err)
		}
	})

	if err := r.Apply(ctx); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// A no-op re-plan proves the container was created and matches intent
	// (bpg refreshes real state from the node during plan).
	changed, err := r.Plan(ctx)
	if err != nil {
		t.Fatalf("post-apply Plan: %v", err)
	}
	if changed {
		t.Errorf("post-apply plan shows a diff; create is not idempotent")
	}
}
