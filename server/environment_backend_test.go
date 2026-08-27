// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"testing"

	"github.com/pelican/wings/config"
	"github.com/pelican/wings/environment"
	"github.com/pelican/wings/environment/lxc"
)

func TestServerToLXCSpec(t *testing.T) {
	s := &Server{}
	s.cfg.Pid = 7
	s.cfg.Uuid = "abc123"
	s.cfg.Container.Image = "ghcr.io/pelican-eggs/yolks:java_21"
	s.cfg.Build = environment.Limits{CpuLimit: 250, MemoryLimit: 2048, Swap: 512, DiskSpace: 10240}

	px := config.ProxmoxConfiguration{
		Node:         "pve1",
		Storage:      "local-zfs",
		Bridge:       "vmbr1",
		Vlan:         39,
		Unprivileged: true,
		VmidBase:     100000,
	}

	spec := serverToLXCSpec(s, px)

	if spec.VMID != 100007 {
		t.Errorf("VMID = %d, want VmidBase+Pid = 100007", spec.VMID)
	}
	if spec.Cores != 3 {
		t.Errorf("Cores = %d, want ceil(250/100) = 3", spec.Cores)
	}
	if spec.RootSizeGiB != 10 {
		t.Errorf("RootSizeGiB = %d, want ceil(10240/1024) = 10", spec.RootSizeGiB)
	}
	if spec.MemoryMiB != 2048 || spec.SwapMiB != 512 {
		t.Errorf("mem/swap = %d/%d, want 2048/512", spec.MemoryMiB, spec.SwapMiB)
	}
	if spec.Node != "pve1" || spec.RootDatastore != "local-zfs" || spec.Bridge != "vmbr1" || spec.VLAN != 39 {
		t.Errorf("placement/network mismatch: %+v", spec)
	}
	// The egg's OCI image is the container base; TemplateFileID is resolved from it
	// at Create time, so it is deliberately empty on the mapped spec.
	if spec.Image != "ghcr.io/pelican-eggs/yolks:java_21" {
		t.Errorf("Image = %q, want the egg OCI image", spec.Image)
	}
	if spec.TemplateFileID != "" {
		t.Errorf("TemplateFileID = %q, want empty (resolved from Image at Create)", spec.TemplateFileID)
	}
	if !spec.Unprivileged || !spec.HostManaged {
		t.Errorf("want an unprivileged, host-managed app container: %+v", spec)
	}
	if spec.Hostname != "penguin-7" {
		t.Errorf("Hostname = %q, want penguin-7", spec.Hostname)
	}
	if spec.IPv4.Address != lxc.DHCPAddress {
		t.Errorf("IPv4 = %q, want dhcp", spec.IPv4.Address)
	}
	// Renderable once the image has resolved to a template volid.
	spec.TemplateFileID = "local:vztmpl/ghcr_io_pelican_eggs_yolks_java_21.tar"
	if err := spec.Validate(); err != nil {
		t.Errorf("resolved spec should be renderable: %v", err)
	}
}

func TestServerToLXCSpecStaticIP(t *testing.T) {
	s := &Server{}
	s.cfg.Pid = 3
	s.cfg.Uuid = "srv"
	s.cfg.Container.Image = "ghcr.io/pelican-eggs/games:java"
	s.cfg.Allocations = environment.Allocations{
		DefaultMapping: &environment.DefaultAllocationMapping{Ip: "10.0.39.5", Port: 25565},
	}

	px := config.ProxmoxConfiguration{
		Node:         "pve1",
		Storage:      "local-zfs",
		Bridge:       "vmbr0",
		Gateway:      "10.0.39.1",
		SubnetPrefix: 24,
		VmidBase:     100000,
	}

	spec := serverToLXCSpec(s, px)

	if spec.Image != "ghcr.io/pelican-eggs/games:java" {
		t.Errorf("Image = %q, want the egg OCI image", spec.Image)
	}
	if spec.IPv4.Address != "10.0.39.5/24" || spec.IPv4.Gateway != "10.0.39.1" {
		t.Errorf("ipv4 = %+v, want static 10.0.39.5/24 gw 10.0.39.1", spec.IPv4)
	}
	spec.TemplateFileID = "local:vztmpl/x.tar"
	if err := spec.Validate(); err != nil {
		t.Errorf("static-IP spec should be renderable: %v", err)
	}
}
