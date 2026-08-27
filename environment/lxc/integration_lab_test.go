// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package lxc

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestLabOCILifecycle exercises the full OCI-image create path against a live PVE
// node: pull the egg image (EnsureOCIImage), tofu-apply an LXC created from it via
// the bpg provider, confirm the container exists, then destroy it. It proves that
// bpg accepts an OCI-derived vztmpl + host_managed networking end to end.
//
// Skipped unless PENGUIN_LAB_ENDPOINT and PENGUIN_LAB_TOKEN_FILE are set; node,
// storages, image, and vmid have generic defaults overridable via PENGUIN_LAB_*:
//
//	PENGUIN_LAB_ENDPOINT=https://your-node:8006/ \
//	PENGUIN_LAB_TOKEN_FILE=/path/to/token PENGUIN_LAB_NODE=pve \
//	go test -tags integration -run TestLabOCILifecycle ./environment/lxc/ -v
func TestLabOCILifecycle(t *testing.T) {
	endpoint := os.Getenv("PENGUIN_LAB_ENDPOINT")
	tokenFile := os.Getenv("PENGUIN_LAB_TOKEN_FILE")
	if endpoint == "" || tokenFile == "" {
		t.Skip("set PENGUIN_LAB_ENDPOINT and PENGUIN_LAB_TOKEN_FILE to run")
	}
	raw, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatalf("read token: %v", err)
	}
	token := strings.TrimSpace(string(raw))

	node := labEnv("PENGUIN_LAB_NODE", "pve")
	rootStore := labEnv("PENGUIN_LAB_STORAGE_ROOT", "local-lvm")
	imgStore := labEnv("PENGUIN_LAB_STORAGE_IMAGE", "local")
	image := labEnv("PENGUIN_LAB_IMAGE", "ghcr.io/pelican-eggs/yolks:java_21")
	vmid, _ := strconv.Atoi(labEnv("PENGUIN_LAB_VMID", "990310"))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	power, err := NewPVEClient(PVEClientConfig{Endpoint: endpoint, APIToken: token, Insecure: true})
	if err != nil {
		t.Fatalf("pve client: %v", err)
	}

	volid, err := power.EnsureOCIImage(ctx, node, imgStore, image)
	if err != nil {
		t.Fatalf("ensure oci image: %v", err)
	}
	t.Logf("egg image %q -> %s", image, volid)

	execPath, err := LookupExecPath()
	if err != nil {
		t.Fatalf("locate tofu: %v", err)
	}
	runner, err := NewRunner(RunnerConfig{ExecPath: execPath, WorkDir: t.TempDir(), APIToken: token})
	if err != nil {
		t.Fatalf("runner: %v", err)
	}

	spec := LXCSpec{
		Node:           node,
		VMID:           vmid,
		Hostname:       "penguin-labtest",
		TemplateFileID: volid,
		Unprivileged:   true,
		Features:       Features{Nesting: true},
		Cores:          2,
		MemoryMiB:      2048,
		RootDatastore:  rootStore,
		RootSizeGiB:    8,
		Bridge:         "vmbr0",
		HostManaged:    true,
		IPv4:           IPv4Config{Address: DHCPAddress},
	}
	provider := ProviderConfig{Endpoint: endpoint, Insecure: true}

	if err := runner.WriteConfig(spec, provider); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := runner.Init(ctx); err != nil {
		t.Fatalf("tofu init: %v", err)
	}
	t.Cleanup(func() {
		// The server layer stops a server before deleting it; mirror that so tofu
		// destroy is not racing a running container for its config lock.
		cctx, ccancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer ccancel()
		_ = power.Stop(cctx, node, vmid)
		_ = power.WaitForStatus(cctx, node, vmid, StatusStopped)
		// Let the stop task release the PVE config lock before destroy (the lab CPU
		// is slow enough that back-to-back stop+destroy can race the flock).
		time.Sleep(8 * time.Second)
		if err := runner.Destroy(context.Background()); err != nil {
			t.Errorf("destroy: %v", err)
		}
	})
	if err := runner.Apply(ctx); err != nil {
		t.Fatalf("tofu apply (create from OCI image): %v", err)
	}

	st, err := power.Status(ctx, node, vmid)
	if err != nil {
		t.Fatalf("status after create: %v", err)
	}
	t.Logf("container %d created from OCI image (status=%q)", vmid, st.Status)
}

func labEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
