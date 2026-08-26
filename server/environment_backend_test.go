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
	s.cfg.Build = environment.Limits{CpuLimit: 250, MemoryLimit: 2048, Swap: 512, DiskSpace: 10240}

	px := config.ProxmoxConfiguration{
		Node:         "pve1",
		Storage:      "local-zfs",
		Bridge:       "vmbr1",
		Vlan:         39,
		Template:     "local:vztmpl/debian-13.tar.zst",
		OsType:       "debian",
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
	if spec.TemplateFileID != "local:vztmpl/debian-13.tar.zst" || spec.OSType != "debian" || !spec.Unprivileged {
		t.Errorf("template/os mismatch: %+v", spec)
	}
	if spec.Hostname != "penguin-7" {
		t.Errorf("Hostname = %q, want penguin-7", spec.Hostname)
	}
	if spec.IPv4.Address != lxc.DHCPAddress {
		t.Errorf("IPv4 = %q, want dhcp", spec.IPv4.Address)
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("mapped spec should be renderable: %v", err)
	}
}
