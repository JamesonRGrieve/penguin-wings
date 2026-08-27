# Penguin Wings

Penguin Wings is the node daemon for **Penguin Panel** — a rebasable soft-fork of
[Pelican Wings](https://github.com/pelican-dev/wings) that replaces the Docker
container backend with **direct Proxmox LXC** provisioning driven by **embedded
OpenTofu**. AGPL-3.0-or-later.

> Read the workspace `/home/jameson/Source/CLAUDE.md` and the user tenets first.
> Go standard: `/home/jameson/Source/ai-prompts/go.md`. Tofu standard:
> `/home/jameson/Source/ai-prompts/tofu.md`. This file wins on Penguin specifics.

## Product shape

Penguin = **Penguin Panel** (Laravel/Filament control plane, the source of truth)
+ **Penguin Wings** (this Go daemon, the realization engine). A "server" is a
game/app server the panel declares and Wings makes real as a **persistent Proxmox
LXC**. Penguin must **stand alone** for downstream operators — no NetBox, no
dependency on any private infra.

## Core architecture (settled)

- **Central daemon, remote Proxmox.** Wings does NOT run on the hypervisor; it
  drives Proxmox over the API. v1 targets the **PVE node API**.
- **Embedded OpenTofu is the realization engine.** Wings drives `tofu` in-process
  (tfexec / terraform-exec) and owns **one Tofu workspace + state per server**.
  The upstream `environment/docker` package is replaced by an `environment/lxc`
  that renders an `LXCSpec` to HCL and reconciles it.
- **Provider: `bpg/proxmox` ONLY** — the best-maintained Proxmox provider; MPL-2.0,
  and Tofu providers are separate gRPC binaries so there is no AGPL-combination
  concern. An LXC guest is a `proxmox_virtual_environment_container`. Renderers sit
  behind a `NodeBackend` interface so a future backend drops in without touching
  callers.
- **The egg's OCI image is the container base.** Each Pelican/Pterodactyl egg
  names a `docker_image`; Wings pulls it onto storage (`oci-registry-pull`,
  idempotent, keyed on the image ref) and creates the persistent LXC **directly
  from that image** via bpg (PVE 9.1+ OCI application containers). The image's own
  runtime and non-root user apply as-is — **nothing per-egg is baked or
  configured, and there is no universal base template.** The egg's install script
  and startup command drive the game (Phase 5). No imperative bootstrapping in Go.
- **Persistent lifecycle.** An LXC is created on install and destroyed on delete.
  `Start`/`Stop`/`Restart` are **power-state changes** (PVE start/stop), never
  recreation.
- **PVE-native process visibility — no in-container agent.** Because the OCI image
  is self-sufficient, Wings needs nothing running inside the container. Console
  (PVE `termproxy`), metrics (`status/current` + `rrddata`), and run state all come
  from the same PVE API Wings already drives. (An earlier in-container
  `penguin-agent` was dropped as redundant once the egg's OCI image — not a
  hand-built base template — became the container base; PVE's metrics are
  container-wide via cgroups, more accurate than a per-process agent.)
- **Egg install runs over a scoped node channel — not root, not an agent.** The
  egg's own install script must run *inside* the freshly-created OCI container as
  container-root, which the PVE REST API cannot do — so Wings uses a **scoped
  `penguin@pam` node account**: an SSH key whose `sudo` is restricted by a wrapper
  (`/usr/local/bin/penguin-pct`) to only `pct exec`/`pct push` over Penguin's vmid
  range, alongside the PVE API token used for lifecycle. **Never hypervisor
  root.** Install sequence (`environment/lxc/install.go`): create the CT from the
  egg image → override the entrypoint to a keepalive (`sleep`) and Start so
  exec/push work → `pct push` the egg install script → `pct exec` a wrapper that
  writes `/etc/resolv.conf`, symlinks `/mnt/server`→`/home/container`, exports the
  egg variables and runs the egg's `install.sh` → chown to the `container` user →
  install the run-script (the egg `STARTUP`) and point the entrypoint at it →
  Stop. The next Start boots straight into the game. **Nothing per-egg is edited.**

## How it maps onto the existing daemon

The daemon's backend contract is `environment.ProcessEnvironment`
(`environment/environment.go`). The LXC environment implements the **same
interface** so it drops into the existing `server/` lifecycle and `router/` with
minimal disturbance (good for clean rebases). The interface cleaves in two:

| Interface methods | Realized by |
|---|---|
| `Create` `Destroy` `Exists` `IsRunning` `Start` `Stop` `WaitForStop` `Terminate` `InSituUpdate` | Embedded Tofu + bpg (infra, incl. OCI image pull) + PVE power API |
| `Attach` `SendCommand` `Readlog` `ExitState` `SetLogCallback` `Uptime` | PVE API — console via `termproxy`, uptime/state via `status/current`, exit best-effort off container state |
| `Type` `Config` `Events` `State`/`SetState` | Reused daemon plumbing (`Type()` → `"lxc"`) |

## Realization gotchas (lab-proven on lab-primus)

Hard-won specifics the create→install→run path depends on — each cost a debugging
pass, so they live here as invariants:

- **bpg `environment_variables` silently no-ops** for OCI containers. Egg
  variables reach the game only via the install wrapper's `export`s and the
  baked-in run-script — never via bpg env.
- **OCI (unmanaged) containers get no resolver from PVE** (`initialization.dns`
  does not apply), so the install wrapper writes `/etc/resolv.conf` itself before
  the egg script fetches game files. A game CT that "can't resolve host" is this.
- **Embedded tofu must not hit the public registry.** A filesystem provider
  mirror is configured via `/root/.tofurc` with `HOME=/root` — the runner env
  strips `TF_*`, so `TF_CLI_CONFIG_FILE` is unavailable.
- **Redeploying the Wings binary into its running CT:** `pct push` into a running
  CT silently fails (text-file-busy) and leaves a **stale** binary. Always
  `systemctl stop wings` → `pct push` → `systemctl start wings`, then verify the
  in-CT inode changed.
- **The lab bridge (`vmbr0`) is a LIVE `/24`, not an isolated net.** CT IPs must
  be chosen from genuinely-free addresses (ping/ARP-sweep first). A collision
  surfaces **indirectly** — as a Panel↔Wings `ConnectionException` (HTTP 500) from
  contested/stale ARP, or the game port simply never binding — **never** as an
  obvious duplicate-IP error. Current lab layout: Wings `192.168.2.231`, Panel
  `192.168.2.242`, game CTs `192.168.2.232` (the allocation IP becomes the static
  CT IP via `serverToLXCSpec`).

## Source of truth

Penguin **Panel** is the source of truth for server intent. Tofu state is the
realization record — one workspace per server. **No NetBox.**

## Soft-fork discipline

- `upstream` remote = `pelican-dev/wings`. Keep the Penguin rework isolated so
  upstream rebases stay clean: prefer new packages/files over in-place rewrites.
- Go **module path stays `github.com/pelican/wings` for now** — a deep
  namespace/module rename is deferred until divergence makes rebasing moot.
- New/modified source files carry `// SPDX-License-Identifier: AGPL-3.0-or-later`.
- Relicensed MIT→AGPL; upstream MIT attribution preserved in `NOTICE` (required by
  the MIT terms — do not remove).

## Quality gates (workspace §8)

gofmt/goimports clean, `go vet` + `golangci-lint` clean, full `go test ./...`
green, typing/lint ratchets never regress — enforced by a pre-commit hook wired at
bootstrap. `--no-verify` forbidden without explicit authorization.

## Phase plan

0. **Fork + plumbing** — DONE: cloned with full history, `upstream` remote,
   AGPL `LICENSE` + MIT `NOTICE`, this `CLAUDE.md`, project memory. Toolchain
   verified (Go 1.25.0, OpenTofu 1.11.5).
1. **bpg spike** — DONE: embedded `tfexec` stands up + tears down one persistent
   LXC created **from an egg's OCI image**, verified on lab PVE 9.2 by
   `TestLabOCILifecycle`; `proxmox_virtual_environment_container` HCL, bpg auth,
   and the scoped `Penguin` token all nailed (see `README.md`).
2. **Wings core** — `environment/lxc` (`LXCSpec` + JSON renderer + lifecycle)
   replaces the Docker environment behind `ProcessEnvironment`. Container is
   created from the egg image (`EnsureOCIImage` → bpg). **DONE**, including the
   game-launch (see Phase 5).
3. **PVE-native process I/O** — console (`termproxy`), metrics, and exit state via
   the PVE API; no in-container agent. Console wiring is the open item.
4. **Panel changes** — see `penguin-panel/CLAUDE.md`.
5. **v1 hardening** — egg install-script + startup-command execution (the
   game-launch) **built** (`environment/lxc/install.go`, run over the scoped
   `penguin@pam` channel) and **proven end to end on lab-primus** via the Panel
   application API: create → Wings pull+create+install → `start_on_completion`
   boots the run-script entrypoint → the game binds its port. Remaining: PVE
   console wiring, a broader compatibility pass across eggs, and tests/docs.

## Open / parked decisions

- **Penguin git remote/hosting** — `origin` =
  `github.com/JamesonRGrieve/penguin-wings` (push target); `upstream` =
  `pelican-dev/wings` for clean rebases.
- **Game-launch (Phase 5)** — **DONE.** The egg's install script and startup
  command run in the OCI container with no agent, over the scoped `penguin@pam`
  node channel (`environment/lxc/install.go`): the install path writes game files
  into the rootfs and installs the run-script as the entrypoint; the run path is
  that entrypoint (the egg `STARTUP`), booted by `start_on_completion`. Proven end
  to end on lab-primus through the Panel application API.
- **PVE console (termproxy)** — OPEN. `Attach`/`SendCommand` return
  `ErrConsolePending` until the `termproxy`/`vncwebsocket` protocol is wired;
  `ExitState` is best-effort (a stopped container reports a clean exit).
- **PDM (Proxmox Datacenter Manager)** — documented target, **PARKED**, not built
  in v1. Its guest-create API surface is unverified; if it can't create guests the
  eventual PDM backend routes to the resolved PVE node API. Keep the `NodeBackend`
  seam so a PDM backend (and/or an in-house PDM provider) slots in later.
- **Deep module/namespace rename** and **user-visible rebrand** — deferred until
  after the Phase 1 technical proof (rebranding an unproven fork is premature).
- **Pre-commit gates** — not yet wired.
