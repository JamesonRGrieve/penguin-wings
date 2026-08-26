// SPDX-License-Identifier: AGPL-3.0-or-later

package lxc

import (
	"encoding/json"
	"fmt"
)

// ignoreStartedAttr is the resource attribute whose drift the rendered container
// ignores: runtime power state is owned by Wings (via the PVE API), not by tofu.
const ignoreStartedAttr = "started"

// Terraform JSON configuration syntax. Blocks that are singletons in the bpg
// schema (cpu, memory, disk, operating_system, initialization, features) are
// encoded as JSON objects; repeatable blocks (network_interface, mount_point,
// ip_config) as JSON arrays — both accepted by OpenTofu.
type tfDocument struct {
	Terraform tfTerraform                       `json:"terraform"`
	Provider  map[string]tfProviderConfig       `json:"provider"`
	Resource  map[string]map[string]tfContainer `json:"resource"`
}

type tfTerraform struct {
	RequiredVersion   string                        `json:"required_version"`
	RequiredProviders map[string]tfRequiredProvider `json:"required_providers"`
}

type tfRequiredProvider struct {
	Source  string `json:"source"`
	Version string `json:"version"`
}

type tfProviderConfig struct {
	Endpoint string `json:"endpoint"`
	Insecure bool   `json:"insecure,omitempty"`
	MinTLS   string `json:"min_tls,omitempty"`
}

type tfContainer struct {
	NodeName     string   `json:"node_name"`
	VMID         int      `json:"vm_id,omitempty"`
	Description  string   `json:"description,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	Unprivileged bool     `json:"unprivileged,omitempty"`
	Started      bool     `json:"started"`
	StartOnBoot  bool     `json:"start_on_boot"`

	CPU             tfCPU       `json:"cpu"`
	Memory          tfMemory    `json:"memory"`
	Disk            tfDisk      `json:"disk"`
	OperatingSystem tfOS        `json:"operating_system"`
	Features        *tfFeatures `json:"features,omitempty"`

	NetworkInterface []tfNIC   `json:"network_interface"`
	MountPoint       []tfMount `json:"mount_point,omitempty"`

	Initialization tfInitialization `json:"initialization"`

	Lifecycle tfLifecycle `json:"lifecycle"`
}

type tfCPU struct {
	Cores int `json:"cores,omitempty"`
}

type tfMemory struct {
	Dedicated int `json:"dedicated,omitempty"`
	Swap      int `json:"swap,omitempty"`
}

type tfDisk struct {
	DatastoreID string `json:"datastore_id,omitempty"`
	Size        int    `json:"size,omitempty"`
}

type tfOS struct {
	TemplateFileID string `json:"template_file_id"`
	Type           string `json:"type,omitempty"`
}

type tfFeatures struct {
	Nesting bool `json:"nesting,omitempty"`
	FUSE    bool `json:"fuse,omitempty"`
	KeyCtl  bool `json:"keyctl,omitempty"`
}

type tfNIC struct {
	Name       string `json:"name"`
	Bridge     string `json:"bridge,omitempty"`
	VLANID     int    `json:"vlan_id,omitempty"`
	MACAddress string `json:"mac_address,omitempty"`
}

type tfMount struct {
	Volume string `json:"volume"`
	Path   string `json:"path"`
	Size   string `json:"size,omitempty"`
	Backup bool   `json:"backup,omitempty"`
}

type tfInitialization struct {
	Hostname    string         `json:"hostname,omitempty"`
	DNS         *tfDNS         `json:"dns,omitempty"`
	IPConfig    []tfIPConfig   `json:"ip_config,omitempty"`
	UserAccount *tfUserAccount `json:"user_account,omitempty"`
}

type tfDNS struct {
	Domain  string   `json:"domain,omitempty"`
	Servers []string `json:"servers,omitempty"`
}

type tfIPConfig struct {
	IPv4 *tfIPv4 `json:"ipv4,omitempty"`
}

type tfIPv4 struct {
	Address string `json:"address,omitempty"`
	Gateway string `json:"gateway,omitempty"`
}

type tfUserAccount struct {
	Keys []string `json:"keys,omitempty"`
}

type tfLifecycle struct {
	IgnoreChanges []string `json:"ignore_changes,omitempty"`
}

// Render validates the spec and provider config and returns the bytes of a
// single OpenTofu JSON configuration file (ConfigFileName) that declares one
// persistent LXC. The output is secret-free: the provider's API token is not
// included and must be supplied via PROXMOX_VE_API_TOKEN at apply time.
func Render(spec LXCSpec, provider ProviderConfig) ([]byte, error) {
	spec = spec.withDefaults()
	provider = provider.withDefaults()
	if err := provider.validate(); err != nil {
		return nil, err
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}

	doc := tfDocument{
		Terraform: tfTerraform{
			RequiredVersion: provider.TofuVersion,
			RequiredProviders: map[string]tfRequiredProvider{
				"proxmox": {Source: provider.Source, Version: provider.Version},
			},
		},
		Provider: map[string]tfProviderConfig{
			"proxmox": {
				Endpoint: provider.Endpoint,
				Insecure: provider.Insecure,
				MinTLS:   provider.MinTLS,
			},
		},
		Resource: map[string]map[string]tfContainer{
			resourceType: {resourceName: renderContainer(spec)},
		},
	}

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal tofu json: %w", err)
	}
	return append(out, '\n'), nil
}

func renderContainer(spec LXCSpec) tfContainer {
	c := tfContainer{
		NodeName:     spec.Node,
		VMID:         spec.VMID,
		Description:  spec.Description,
		Tags:         spec.Tags,
		Unprivileged: spec.Unprivileged,
		// Created stopped: Wings powers the server on via the PVE API. Drift on
		// the live power state is ignored below.
		Started:     false,
		StartOnBoot: spec.StartOnBoot,
		CPU:         tfCPU{Cores: spec.Cores},
		Memory:      tfMemory{Dedicated: spec.MemoryMiB, Swap: spec.SwapMiB},
		Disk:        tfDisk{DatastoreID: spec.RootDatastore, Size: spec.RootSizeGiB},
		OperatingSystem: tfOS{
			TemplateFileID: spec.TemplateFileID,
			Type:           spec.OSType,
		},
		Features: renderFeatures(spec.Features),
		NetworkInterface: []tfNIC{{
			Name:       spec.NetworkName,
			Bridge:     spec.Bridge,
			VLANID:     spec.VLAN,
			MACAddress: spec.MACAddress,
		}},
		MountPoint:     renderMounts(spec.Mounts),
		Initialization: renderInitialization(spec),
		Lifecycle:      tfLifecycle{IgnoreChanges: []string{ignoreStartedAttr}},
	}
	return c
}

func renderFeatures(f Features) *tfFeatures {
	if !f.Nesting && !f.FUSE && !f.KeyCtl {
		return nil
	}
	return &tfFeatures{Nesting: f.Nesting, FUSE: f.FUSE, KeyCtl: f.KeyCtl}
}

func renderMounts(mounts []Mount) []tfMount {
	if len(mounts) == 0 {
		return nil
	}
	out := make([]tfMount, 0, len(mounts))
	for _, m := range mounts {
		out = append(out, tfMount{
			Volume: m.Volume,
			Path:   m.Path,
			Size:   m.Size,
			Backup: m.Backup,
		})
	}
	return out
}

func renderInitialization(spec LXCSpec) tfInitialization {
	init := tfInitialization{
		Hostname: spec.Hostname,
		IPConfig: []tfIPConfig{{IPv4: renderIPv4(spec.IPv4)}},
	}
	if len(spec.Nameservers) > 0 || spec.SearchDomain != "" {
		init.DNS = &tfDNS{Domain: spec.SearchDomain, Servers: spec.Nameservers}
	}
	if len(spec.SSHKeys) > 0 {
		init.UserAccount = &tfUserAccount{Keys: spec.SSHKeys}
	}
	return init
}

func renderIPv4(cfg IPv4Config) *tfIPv4 {
	if cfg.Address == DHCPAddress {
		return &tfIPv4{Address: DHCPAddress}
	}
	return &tfIPv4{Address: cfg.Address, Gateway: cfg.Gateway}
}
