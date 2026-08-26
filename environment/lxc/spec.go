// SPDX-License-Identifier: AGPL-3.0-or-later

// Package lxc renders Penguin server intent into OpenTofu configuration that the
// bpg/proxmox provider realizes as a persistent Proxmox LXC container, and (in a
// later phase) implements the Wings environment.ProcessEnvironment interface on
// top of it.
//
// Design: Penguin Wings runs centrally and drives Proxmox over the API. A server
// is a persistent unprivileged LXC created from a base OS template. OpenTofu (the
// bpg provider) owns create/destroy and the container's declared shape, while
// runtime power state (start/stop) is driven separately through the PVE API — so
// the rendered resource ignores changes to the started attribute. The provider's
// API token is never rendered into configuration; it is supplied to the tofu
// process via PROXMOX_VE_API_TOKEN at apply time.
package lxc

import (
	"errors"
	"fmt"
	"strings"
)

// Defaults applied to a spec / provider config when a field is left zero.
const (
	DefaultProviderSource  = "bpg/proxmox"
	DefaultProviderVersion = "~> 0.111"
	DefaultTofuVersion     = "~> 1.11"
	DefaultMinTLS          = "1.3"

	DefaultNetworkName   = "veth0"
	DefaultBridge        = "vmbr0"
	DefaultRootDatastore = "local-lvm"
	DefaultOSType        = "unmanaged"

	DefaultCores       = 1
	DefaultMemoryMiB   = 512
	DefaultRootSizeGiB = 8

	// DHCPAddress is the sentinel value for a DHCP-configured interface.
	DHCPAddress = "dhcp"

	// ConfigFileName is the generated OpenTofu JSON config the harness writes
	// into a server's workspace.
	ConfigFileName = "main.tf.json"

	// resourceType is the bpg container resource type. resourceName is the
	// resource local name — one workspace holds exactly one server, so a fixed
	// name is correct and keeps the address stable across renders.
	resourceType = "proxmox_virtual_environment_container"
	resourceName = "server"
)

// ErrInvalidSpec wraps every LXCSpec validation failure; callers may branch on it
// with errors.Is.
var ErrInvalidSpec = errors.New("invalid lxc spec")

// ErrInvalidProvider wraps every ProviderConfig validation failure.
var ErrInvalidProvider = errors.New("invalid provider config")

// ProviderConfig is the non-secret bpg/proxmox provider configuration. The API
// token is deliberately absent: it is passed to the tofu process through the
// PROXMOX_VE_API_TOKEN environment variable at apply and never written to disk.
type ProviderConfig struct {
	// Endpoint is the PVE API base URL, e.g. "https://node.example:8006/".
	Endpoint string
	// Insecure skips TLS verification (self-signed PVE certs).
	Insecure bool
	// MinTLS is the minimum TLS version; defaults to DefaultMinTLS.
	MinTLS string

	// Source / Version pin the provider in required_providers.
	Source  string
	Version string
	// TofuVersion pins required_version for the OpenTofu CLI itself.
	TofuVersion string
}

// IPv4Config is the static or DHCP IPv4 setup for the container's primary NIC.
type IPv4Config struct {
	// Address is a CIDR (e.g. "10.0.39.5/24") or the literal "dhcp".
	Address string
	// Gateway is required for a static Address and must be empty for DHCP.
	Gateway string
}

// Mount is a persistent data volume attached to the container (e.g. game-server
// data that must survive a rebuild of the rootfs).
type Mount struct {
	// Volume is a storage ID (new managed volume), a "storage:volume" reference
	// to an existing volume, or an absolute host path for a bind mount.
	Volume string
	// Path is the absolute mount path inside the container.
	Path string
	// Size (e.g. "10G") is required when Volume names a storage for a new volume.
	Size string
	// Backup includes this mount in vzdump backups.
	Backup bool
}

// Features toggles container feature flags.
type Features struct {
	Nesting bool
	FUSE    bool
	KeyCtl  bool
}

// LXCSpec is the logical description of one Penguin server realized as a
// persistent Proxmox LXC. It carries no secrets.
type LXCSpec struct {
	// Placement / identity.
	Node string // PVE node name (required).
	VMID int    // Proxmox VMID; 0 lets the provider assign one.

	Hostname    string // Required; a valid DNS label.
	Description string
	Tags        []string

	// Base image.
	TemplateFileID string // operating_system.template_file_id (required).
	OSType         string // operating_system.type; defaults to DefaultOSType.
	Unprivileged   bool
	Features       Features

	// Resources.
	Cores         int    // cpu.cores; defaults to DefaultCores.
	MemoryMiB     int    // memory.dedicated; defaults to DefaultMemoryMiB.
	SwapMiB       int    // memory.swap.
	RootDatastore string // disk.datastore_id; defaults to DefaultRootDatastore.
	RootSizeGiB   int    // disk.size; defaults to DefaultRootSizeGiB.

	// Persistent data volumes.
	Mounts []Mount

	// Network.
	NetworkName  string // defaults to DefaultNetworkName.
	Bridge       string // defaults to DefaultBridge.
	VLAN         int    // 802.1q tag; 0 = untagged.
	MACAddress   string // optional stable MAC for the NIC.
	IPv4         IPv4Config
	Nameservers  []string
	SearchDomain string

	// Provisioning (public material only — SSH public keys).
	SSHKeys []string

	// Power policy. Runtime power is driven by Wings via the PVE API; only the
	// boot policy is declared here. The rendered resource is created stopped and
	// ignores drift on the live power state.
	StartOnBoot bool
}

func (c ProviderConfig) withDefaults() ProviderConfig {
	if c.MinTLS == "" {
		c.MinTLS = DefaultMinTLS
	}
	if c.Source == "" {
		c.Source = DefaultProviderSource
	}
	if c.Version == "" {
		c.Version = DefaultProviderVersion
	}
	if c.TofuVersion == "" {
		c.TofuVersion = DefaultTofuVersion
	}
	return c
}

func (c ProviderConfig) validate() error {
	if strings.TrimSpace(c.Endpoint) == "" {
		return fmt.Errorf("%w: endpoint is required", ErrInvalidProvider)
	}
	if !strings.HasPrefix(c.Endpoint, "http://") && !strings.HasPrefix(c.Endpoint, "https://") {
		return fmt.Errorf("%w: endpoint %q must be an http(s) URL", ErrInvalidProvider, c.Endpoint)
	}
	return nil
}

func (s LXCSpec) withDefaults() LXCSpec {
	if s.OSType == "" {
		s.OSType = DefaultOSType
	}
	if s.Cores == 0 {
		s.Cores = DefaultCores
	}
	if s.MemoryMiB == 0 {
		s.MemoryMiB = DefaultMemoryMiB
	}
	if s.RootDatastore == "" {
		s.RootDatastore = DefaultRootDatastore
	}
	if s.RootSizeGiB == 0 {
		s.RootSizeGiB = DefaultRootSizeGiB
	}
	if s.NetworkName == "" {
		s.NetworkName = DefaultNetworkName
	}
	if s.Bridge == "" {
		s.Bridge = DefaultBridge
	}
	return s
}

// Validate reports the first problem that would make the spec unrenderable or
// produce an invalid container. All failures wrap ErrInvalidSpec.
func (s LXCSpec) Validate() error {
	if strings.TrimSpace(s.Node) == "" {
		return fmt.Errorf("%w: node is required", ErrInvalidSpec)
	}
	if strings.TrimSpace(s.TemplateFileID) == "" {
		return fmt.Errorf("%w: template_file_id is required", ErrInvalidSpec)
	}
	if strings.TrimSpace(s.Hostname) == "" {
		return fmt.Errorf("%w: hostname is required", ErrInvalidSpec)
	}
	if s.VMID < 0 {
		return fmt.Errorf("%w: vm_id %d must not be negative", ErrInvalidSpec, s.VMID)
	}
	if s.Cores < 0 || s.MemoryMiB < 0 || s.SwapMiB < 0 || s.RootSizeGiB < 0 {
		return fmt.Errorf("%w: cpu/memory/disk values must not be negative", ErrInvalidSpec)
	}
	if err := s.validateIPv4(); err != nil {
		return err
	}
	for i, m := range s.Mounts {
		if strings.TrimSpace(m.Volume) == "" {
			return fmt.Errorf("%w: mount %d is missing a volume", ErrInvalidSpec, i)
		}
		if strings.TrimSpace(m.Path) == "" {
			return fmt.Errorf("%w: mount %d is missing a path", ErrInvalidSpec, i)
		}
	}
	return nil
}

func (s LXCSpec) validateIPv4() error {
	addr := strings.TrimSpace(s.IPv4.Address)
	if addr == "" {
		return fmt.Errorf("%w: ipv4 address is required (a CIDR or %q)", ErrInvalidSpec, DHCPAddress)
	}
	if addr == DHCPAddress {
		if strings.TrimSpace(s.IPv4.Gateway) != "" {
			return fmt.Errorf("%w: ipv4 gateway must be empty for dhcp", ErrInvalidSpec)
		}
		return nil
	}
	if !strings.Contains(addr, "/") {
		return fmt.Errorf("%w: static ipv4 address %q must be a CIDR (e.g. 10.0.0.5/24)", ErrInvalidSpec, addr)
	}
	if strings.TrimSpace(s.IPv4.Gateway) == "" {
		return fmt.Errorf("%w: static ipv4 address requires a gateway", ErrInvalidSpec)
	}
	return nil
}
