# Deploying Penguin Wings

Penguin Wings runs centrally and drives Proxmox over the API. A server's game runs
from the egg's own **OCI image** — Wings pulls it onto the node and creates the LXC
directly from it — so there is **no base template and no in-container agent** to
build. This directory holds the pieces needed to stand Wings up.

## 1. Build the binary (linux/amd64)

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/wings ./
```

Wings also needs the `tofu` (OpenTofu) binary on `PATH` — it drives it in-process
to realize each container. The Proxmox node must be **PVE 9.1+** (OCI application
containers).

## 2. Create the Proxmox API token

See the "Proxmox API token" section in the top-level `README.md` — a scoped,
least-privilege `penguin@pve!wings` token (one copy-paste command run as root on
the node). Wings reads it from `proxmox.token`.

## 3. Configure Wings

Minimal `config.yml` for the LXC backend:

```yaml
backend: lxc
proxmox:
  endpoint: "https://node.example:8006/"
  token: "penguin@pve!wings=<secret>"   # or the PROXMOX_VE_API_TOKEN env var
  insecure: true                         # self-signed PVE cert
  node: "pve1"
  storage: "local-lvm"                   # datastore for container rootfs volumes
  image_storage: "local"                 # vztmpl storage egg OCI images are pulled onto
  bridge: "vmbr0"
  vlan: 0                                 # 802.1q tag on the container NIC, 0 = untagged
  unprivileged: true
  vmid_base: 100000                       # vmid = vmid_base + Panel server id
  # Static per-LXC IP from the Panel allocation (else DHCP):
  gateway: ""                             # e.g. 10.0.39.1 — set to enable static IPs
  subnet_prefix: 24
```

The first server on a given egg triggers a one-time `oci-registry-pull` of that
egg's image onto `image_storage`; every later server on the same egg reuses it.

## Networking (Pelican-equivalent)

Each server's container attaches directly to `bridge` (optionally VLAN-tagged) and
gets its allocation IP (a static CIDR from the allocation + `gateway`, or DHCP). The
game binds to that IP:port — reachable by anything that can route to the subnet.
Upstream routing/firewalling is the hoster's responsibility, exactly as with
Pelican/Pterodactyl.
