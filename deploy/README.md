# Deploying Penguin Wings + the base LXC template

Penguin Wings runs centrally and drives Proxmox over the API. This directory holds
the pieces needed to stand it up.

## 1. Build the binaries (linux/amd64)

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/wings ./
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/penguin-agent ./cmd/penguin-agent
```

## 2. Create the Proxmox API token

See the "Proxmox API token" section in the top-level `README.md` — a scoped,
least-privilege `penguin@pve!wings` token. Wings reads it from `proxmox.token`.

## 3. Build the base LXC template

Penguin containers are cloned/created from a base template that carries the
in-container `penguin-agent` and the game-server runtime. To build it:

1. Create a scratch Debian LXC on the node.
2. Push `dist/penguin-agent` → `/usr/local/bin/penguin-agent` (chmod +x) and
   `deploy/penguin-agent.service` → `/root/penguin-agent.service`.
3. Inside the container, with the shared token exported, run
   `deploy/build-base-template.sh` — it installs the agent service, SteamCMD, and
   32-bit libraries.
4. Stop the container and convert it to a template (`pct template <vmid>`), then
   note its volume id for `proxmox.template`.

The **same** `PENGUIN_AGENT_TOKEN` baked into the template must equal Wings'
`proxmox.agent_token` (shared-token model; per-server tokens are a hardening
item).

## 4. Configure Wings

Minimal `config.yml` for the LXC backend:

```yaml
backend: lxc
proxmox:
  endpoint: "https://node.example:8006/"
  token: "penguin@pve!wings=<secret>"   # or PROXMOX_VE_API_TOKEN
  insecure: true                         # self-signed PVE cert
  node: "pve1"
  storage: "local-zfs"
  bridge: "vmbr0"
  vlan: 0                                 # 802.1q tag on the container NIC, 0 = untagged
  template: "local:vztmpl/penguin-base.tar.zst"
  vmid_base: 100000
  agent_port: 8443
  agent_token: "<shared agent token>"
  # Static per-LXC IP from the Panel allocation (else DHCP):
  gateway: ""                             # e.g. 10.0.39.1
  subnet_prefix: 24
  # Per-egg base template overrides (optional):
  template_map: {}                        # {"ghcr.io/pelican-eggs/games:java": "local:vztmpl/java.tar.zst"}
```

## Networking (Pelican-equivalent)

Each server's container attaches directly to `bridge` (optionally VLAN-tagged) and
gets its allocation IP (static CIDR from the allocation + `gateway`, or DHCP). The
game binds to that IP:port — reachable by anything that can route to the subnet.
Upstream routing/firewalling is the hoster's responsibility, exactly as with
Pelican/Pterodactyl.
