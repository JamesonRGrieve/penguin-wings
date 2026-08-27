// SPDX-License-Identifier: AGPL-3.0-or-later

package config

// ProxmoxConfiguration configures the "lxc" backend: how Wings reaches Proxmox
// and the defaults used when realizing a server as a persistent LXC container.
// It is only consulted when Backend is "lxc".
type ProxmoxConfiguration struct {
	// Endpoint is the PVE API base URL, e.g. https://node.example:8006/.
	Endpoint string `json:"endpoint" yaml:"endpoint"`

	// Token is the PVE API token (user@realm!tokenid=secret). It is injected into
	// the tofu process and the PVE power client at runtime and never rendered into
	// generated configuration.
	Token string `json:"token" yaml:"token"`

	// Insecure skips TLS verification for self-signed PVE certificates.
	Insecure bool `json:"insecure" yaml:"insecure"`

	// Node is the target PVE node new containers are created on.
	Node string `json:"node" yaml:"node"`

	// Storage is the datastore for container root filesystems.
	Storage string `default:"local-lvm" json:"storage" yaml:"storage"`

	// ImageStorage is the (vztmpl-capable) storage that egg OCI images are pulled
	// onto and containers are created from. Distinct from Storage: images are
	// file-backed templates, root filesystems are volumes.
	ImageStorage string `default:"local" json:"image_storage" yaml:"image_storage"`

	// Bridge is the network bridge new container NICs attach to.
	Bridge string `default:"vmbr0" json:"bridge" yaml:"bridge"`

	// Vlan tags the container NIC (0 = untagged).
	Vlan int `json:"vlan" yaml:"vlan"`

	// Unprivileged runs containers unprivileged.
	Unprivileged bool `default:"true" json:"unprivileged" yaml:"unprivileged"`

	// VmidBase offsets a server's Panel id to derive a stable, collision-free
	// Proxmox VMID (vmid = VmidBase + server.id).
	VmidBase int `default:"100000" json:"vmid_base" yaml:"vmid_base"`

	// TofuPath overrides the OpenTofu/Terraform binary path; discovered on PATH
	// when empty.
	TofuPath string `json:"tofu_path" yaml:"tofu_path"`

	// StateDirectory holds the per-server OpenTofu workspaces. Defaults to a
	// "tofu" directory under the system root directory when empty.
	StateDirectory string `json:"state_directory" yaml:"state_directory"`

	// Gateway, when set, gives containers a static IPv4 built from the server's
	// default allocation IP and SubnetPrefix with this gateway. Otherwise DHCP.
	Gateway string `json:"gateway" yaml:"gateway"`

	// SubnetPrefix is the CIDR prefix length applied to a static allocation IP.
	SubnetPrefix int `default:"24" json:"subnet_prefix" yaml:"subnet_prefix"`
}
