// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"github.com/pelican/wings/config"
	"github.com/pelican/wings/environment"
	"github.com/pelican/wings/environment/docker"
	"github.com/pelican/wings/environment/lxc"
)

// defaultSubnetPrefix is the CIDR prefix applied to a static allocation IP when
// proxmox.subnet_prefix is unset.
const defaultSubnetPrefix = 24

// configureServerEnvironment builds the ProcessEnvironment for a server based on
// the configured backend. It replaces the previously hard-coded Docker
// construction in InitServer so additional backends slot in without disturbing
// the server bootstrap flow.
func configureServerEnvironment(s *Server, envCfg *environment.Configuration) (environment.ProcessEnvironment, error) {
	switch strings.ToLower(config.Get().Backend) {
	case "", "docker":
		meta := docker.Metadata{Image: s.Config().Container.Image}
		return docker.New(s.ID(), &meta, envCfg)
	case "lxc":
		return newLXCEnvironment(s, envCfg)
	default:
		return nil, fmt.Errorf("server: unknown backend %q", config.Get().Backend)
	}
}

// newLXCEnvironment wires the embedded-Tofu Runner and the PVE power client into
// an LXC environment for a single server.
func newLXCEnvironment(s *Server, envCfg *environment.Configuration) (environment.ProcessEnvironment, error) {
	px := config.Get().Proxmox

	execPath := px.TofuPath
	if execPath == "" {
		p, err := lxc.LookupExecPath()
		if err != nil {
			return nil, err
		}
		execPath = p
	}

	stateDir := px.StateDirectory
	if stateDir == "" {
		stateDir = filepath.Join(config.Get().System.RootDirectory, "tofu")
	}

	runner, err := lxc.NewRunner(lxc.RunnerConfig{
		ExecPath: execPath,
		WorkDir:  filepath.Join(stateDir, s.ID()),
		APIToken: px.Token,
	})
	if err != nil {
		return nil, err
	}

	power, err := lxc.NewPVEClient(lxc.PVEClientConfig{
		Endpoint: px.Endpoint,
		APIToken: px.Token,
		Insecure: px.Insecure,
	})
	if err != nil {
		return nil, err
	}

	spec := serverToLXCSpec(s, px)
	return lxc.New(lxc.Config{
		ID:            s.ID(),
		Configuration: envCfg,
		Node:          px.Node,
		VMID:          spec.VMID,
		Spec:          spec,
		Provider:      lxc.ProviderConfig{Endpoint: px.Endpoint, Insecure: px.Insecure},
		Runner:        runner,
		Power:         power,
		ImageStorage:  px.ImageStorage,
	})
}

// serverToLXCSpec maps a server's Panel-provided configuration (build limits,
// allocations, identity) onto an LXCSpec. The Panel `id` (guaranteed unique)
// yields a stable, collision-free VMID. The egg's own OCI image is the container
// base (Image); the environment pulls it onto storage at Create time, so no base
// template is configured and the image's own runtime and non-root user apply.
func serverToLXCSpec(s *Server, px config.ProxmoxConfiguration) lxc.LXCSpec {
	cfg := s.Config()
	b := cfg.Build

	cores := 0
	if b.CpuLimit > 0 {
		cores = int(math.Ceil(float64(b.CpuLimit) / 100))
	}
	rootGiB := 0
	if b.DiskSpace > 0 {
		rootGiB = int(math.Ceil(float64(b.DiskSpace) / 1024))
	}

	// allocation -> static IPv4 when a gateway is configured, else DHCP.
	ipv4 := lxc.IPv4Config{Address: lxc.DHCPAddress}
	if px.Gateway != "" && cfg.Allocations.DefaultMapping != nil && cfg.Allocations.DefaultMapping.Ip != "" {
		prefix := px.SubnetPrefix
		if prefix <= 0 {
			prefix = defaultSubnetPrefix
		}
		ipv4 = lxc.IPv4Config{
			Address: fmt.Sprintf("%s/%d", cfg.Allocations.DefaultMapping.Ip, prefix),
			Gateway: px.Gateway,
		}
	}

	return lxc.LXCSpec{
		Node:          px.Node,
		VMID:          px.VmidBase + cfg.Pid,
		Hostname:      fmt.Sprintf("penguin-%d", cfg.Pid),
		Description:   "Penguin server " + s.ID(),
		Tags:          []string{"penguin"},
		Image:         cfg.Container.Image,
		Unprivileged:  px.Unprivileged,
		Cores:         cores,
		MemoryMiB:     int(b.MemoryLimit),
		SwapMiB:       int(b.Swap),
		RootDatastore: px.Storage,
		RootSizeGiB:   rootGiB,
		Bridge:        px.Bridge,
		// OCI application containers have no init to configure their own NIC.
		HostManaged: true,
		VLAN:        px.Vlan,
		IPv4:        ipv4,
	}
}
