// SPDX-License-Identifier: AGPL-3.0-or-later

package lxc

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func validSpec() LXCSpec {
	return LXCSpec{
		Node:           "pve1",
		Hostname:       "mc-01",
		TemplateFileID: "local:vztmpl/debian-12-standard_12.7-1_amd64.tar.zst",
		OSType:         "debian",
		Unprivileged:   true,
		IPv4:           IPv4Config{Address: "10.0.39.5/24", Gateway: "10.0.39.1"},
	}
}

func validProvider() ProviderConfig {
	return ProviderConfig{Endpoint: "https://pve1.example:8006/"}
}

func mustRender(t *testing.T, s LXCSpec, p ProviderConfig) (tfDocument, string) {
	t.Helper()
	b, err := Render(s, p)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !json.Valid(b) {
		t.Fatalf("rendered output is not valid JSON:\n%s", b)
	}
	var doc tfDocument
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("unmarshal rendered output: %v", err)
	}
	return doc, string(b)
}

func containerOf(t *testing.T, doc tfDocument) tfContainer {
	t.Helper()
	byName, ok := doc.Resource[resourceType]
	if !ok {
		t.Fatalf("resource type %q missing", resourceType)
	}
	c, ok := byName[resourceName]
	if !ok {
		t.Fatalf("resource %q missing", resourceName)
	}
	return c
}

func TestLXCSpecValidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		mutate  func(*LXCSpec)
		wantErr error
	}{
		{"valid static", func(*LXCSpec) {}, nil},
		{"valid dhcp", func(s *LXCSpec) { s.IPv4 = IPv4Config{Address: DHCPAddress} }, nil},
		{"missing node", func(s *LXCSpec) { s.Node = "" }, ErrInvalidSpec},
		{"missing template", func(s *LXCSpec) { s.TemplateFileID = "" }, ErrInvalidSpec},
		{"missing hostname", func(s *LXCSpec) { s.Hostname = "" }, ErrInvalidSpec},
		{"negative vmid", func(s *LXCSpec) { s.VMID = -1 }, ErrInvalidSpec},
		{"negative cores", func(s *LXCSpec) { s.Cores = -2 }, ErrInvalidSpec},
		{"missing ipv4", func(s *LXCSpec) { s.IPv4 = IPv4Config{} }, ErrInvalidSpec},
		{"static without gateway", func(s *LXCSpec) { s.IPv4 = IPv4Config{Address: "10.0.0.5/24"} }, ErrInvalidSpec},
		{"static not cidr", func(s *LXCSpec) { s.IPv4 = IPv4Config{Address: "10.0.0.5", Gateway: "10.0.0.1"} }, ErrInvalidSpec},
		{"dhcp with gateway", func(s *LXCSpec) { s.IPv4 = IPv4Config{Address: DHCPAddress, Gateway: "10.0.0.1"} }, ErrInvalidSpec},
		{"mount missing volume", func(s *LXCSpec) { s.Mounts = []Mount{{Path: "/data"}} }, ErrInvalidSpec},
		{"mount missing path", func(s *LXCSpec) { s.Mounts = []Mount{{Volume: "local-lvm", Size: "10G"}} }, ErrInvalidSpec},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := validSpec()
			tc.mutate(&s)
			err := s.withDefaults().Validate()
			switch {
			case tc.wantErr == nil && err != nil:
				t.Fatalf("want no error, got %v", err)
			case tc.wantErr != nil && !errors.Is(err, tc.wantErr):
				t.Fatalf("want error wrapping %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestProviderConfigValidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		cfg     ProviderConfig
		wantErr error
	}{
		{"valid", validProvider(), nil},
		{"empty endpoint", ProviderConfig{}, ErrInvalidProvider},
		{"non-http endpoint", ProviderConfig{Endpoint: "pve1.example:8006"}, ErrInvalidProvider},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Render(validSpec(), tc.cfg)
			switch {
			case tc.wantErr == nil && err != nil:
				t.Fatalf("want no error, got %v", err)
			case tc.wantErr != nil && !errors.Is(err, tc.wantErr):
				t.Fatalf("want error wrapping %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestRenderDefaultsAndPowerPolicy(t *testing.T) {
	t.Parallel()
	doc, _ := mustRender(t, validSpec(), validProvider())
	c := containerOf(t, doc)

	if got, want := c.CPU.Cores, DefaultCores; got != want {
		t.Errorf("cores = %d, want default %d", got, want)
	}
	if got, want := c.Memory.Dedicated, DefaultMemoryMiB; got != want {
		t.Errorf("memory = %d, want default %d", got, want)
	}
	if got, want := c.Disk.DatastoreID, DefaultRootDatastore; got != want {
		t.Errorf("disk datastore = %q, want default %q", got, want)
	}
	if got, want := c.Disk.Size, DefaultRootSizeGiB; got != want {
		t.Errorf("disk size = %d, want default %d", got, want)
	}
	if len(c.NetworkInterface) != 1 {
		t.Fatalf("want 1 network_interface, got %d", len(c.NetworkInterface))
	}
	if got, want := c.NetworkInterface[0].Name, DefaultNetworkName; got != want {
		t.Errorf("nic name = %q, want default %q", got, want)
	}
	if got, want := c.NetworkInterface[0].Bridge, DefaultBridge; got != want {
		t.Errorf("nic bridge = %q, want default %q", got, want)
	}

	// Created stopped; runtime power is external, so drift on started is ignored.
	if c.Started {
		t.Errorf("started = true, want false (Wings owns runtime power)")
	}
	if got := c.Lifecycle.IgnoreChanges; len(got) != 1 || got[0] != ignoreStartedAttr {
		t.Errorf("lifecycle.ignore_changes = %v, want [%q]", got, ignoreStartedAttr)
	}
}

func TestRenderStaticVsDHCP(t *testing.T) {
	t.Parallel()

	t.Run("static", func(t *testing.T) {
		t.Parallel()
		doc, _ := mustRender(t, validSpec(), validProvider())
		ip := containerOf(t, doc).Initialization.IPConfig
		if len(ip) != 1 || ip[0].IPv4 == nil {
			t.Fatalf("want one ipv4 ip_config, got %+v", ip)
		}
		if ip[0].IPv4.Address != "10.0.39.5/24" || ip[0].IPv4.Gateway != "10.0.39.1" {
			t.Errorf("ipv4 = %+v, want address/gateway set", ip[0].IPv4)
		}
	})

	t.Run("dhcp omits gateway", func(t *testing.T) {
		t.Parallel()
		s := validSpec()
		s.IPv4 = IPv4Config{Address: DHCPAddress}
		doc, _ := mustRender(t, s, validProvider())
		ip := containerOf(t, doc).Initialization.IPConfig
		if len(ip) != 1 || ip[0].IPv4 == nil {
			t.Fatalf("want one ipv4 ip_config, got %+v", ip)
		}
		if ip[0].IPv4.Address != DHCPAddress || ip[0].IPv4.Gateway != "" {
			t.Errorf("ipv4 = %+v, want dhcp with empty gateway", ip[0].IPv4)
		}
	})
}

func TestRenderNetworkMountsFeatures(t *testing.T) {
	t.Parallel()
	s := validSpec()
	s.VLAN = 39
	s.MACAddress = "BC:24:11:00:00:01"
	s.Bridge = "vmbr1"
	s.Features = Features{Nesting: true}
	s.Mounts = []Mount{{Volume: "local-lvm", Path: "/home/container", Size: "20G", Backup: true}}

	doc, _ := mustRender(t, s, validProvider())
	c := containerOf(t, doc)

	nic := c.NetworkInterface[0]
	if nic.VLANID != 39 || nic.MACAddress != "BC:24:11:00:00:01" || nic.Bridge != "vmbr1" {
		t.Errorf("nic = %+v, want vlan/mac/bridge set", nic)
	}
	if c.Features == nil || !c.Features.Nesting {
		t.Errorf("features = %+v, want nesting true", c.Features)
	}
	if len(c.MountPoint) != 1 {
		t.Fatalf("want 1 mount_point, got %d", len(c.MountPoint))
	}
	if m := c.MountPoint[0]; m.Volume != "local-lvm" || m.Path != "/home/container" || m.Size != "20G" || !m.Backup {
		t.Errorf("mount = %+v, want volume/path/size/backup set", m)
	}
}

func TestRenderNoFeaturesBlockWhenUnset(t *testing.T) {
	t.Parallel()
	doc, _ := mustRender(t, validSpec(), validProvider())
	if c := containerOf(t, doc); c.Features != nil {
		t.Errorf("features = %+v, want omitted when all flags false", c.Features)
	}
}

func TestRenderProviderPinAndSecretFree(t *testing.T) {
	t.Parallel()
	doc, raw := mustRender(t, validSpec(), validProvider())

	prov, ok := doc.Provider["proxmox"]
	if !ok {
		t.Fatalf("provider proxmox missing")
	}
	if prov.Endpoint != "https://pve1.example:8006/" {
		t.Errorf("endpoint = %q, want the configured endpoint", prov.Endpoint)
	}
	rp := doc.Terraform.RequiredProviders["proxmox"]
	if rp.Source != DefaultProviderSource || rp.Version != DefaultProviderVersion {
		t.Errorf("required provider = %+v, want %s %s", rp, DefaultProviderSource, DefaultProviderVersion)
	}

	// The API token must never be rendered into configuration; it is supplied via
	// PROXMOX_VE_API_TOKEN at apply time.
	for _, needle := range []string{"api_token", "token", "password", "secret"} {
		if strings.Contains(strings.ToLower(raw), needle) {
			t.Errorf("rendered config contains %q; must be secret-free:\n%s", needle, raw)
		}
	}
}
