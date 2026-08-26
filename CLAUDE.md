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
- **Provisioning:** base LXC template → declarative persistent LXC → **atomic
  Ansible as a separate declarative step**, used *only* where Tofu can't express
  the config. No imperative bootstrapping in Go.
- **Persistent lifecycle.** An LXC is created on install and destroyed on delete.
  `Start`/`Stop`/`Restart` are **power-state changes** (PVE start/stop), never
  recreation.
- **Detached-daemon bridge — `penguin-agent`.** Central Wings can't touch the
  container's stdio or files, so an in-container agent (shipped in the base
  template) serves console stdio, the SFTP data layer, resource stats, and backup
  hooks back to Wings over one authenticated channel. (Chosen over a PVE-API-only
  bridge, which would make the live console materially worse than upstream.)

## How it maps onto the existing daemon

The daemon's backend contract is `environment.ProcessEnvironment`
(`environment/environment.go`). The LXC environment implements the **same
interface** so it drops into the existing `server/` lifecycle and `router/` with
minimal disturbance (good for clean rebases). The interface cleaves in two:

| Interface methods | Realized by |
|---|---|
| `Create` `Destroy` `Exists` `IsRunning` `Start` `Stop` `WaitForStop` `Terminate` `InSituUpdate` | Embedded Tofu + bpg (infra) + PVE power API |
| `Attach` `SendCommand` `Readlog` `ExitState` `SetLogCallback` `Uptime` | `penguin-agent` (in-container process I/O) |
| `Type` `Config` `Events` `State`/`SetState` | Reused daemon plumbing (`Type()` → `"lxc"`) |

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
1. **bpg spike** — embedded `tfexec` stands up + tears down one persistent LXC
   from a base template; nail the `proxmox_virtual_environment_container` HCL and
   bpg auth model. (Blocked on an operator-named Proxmox target — see below.)
2. **Wings core** — `environment/lxc` (`LXCSpec` + HCL renderer + reconcile loop)
   replaces the Docker environment behind `ProcessEnvironment`.
3. **`penguin-agent`** — the console / SFTP / stats / backup bridge.
4. **Panel changes** — see `penguin-panel/CLAUDE.md`.
5. **v1 hardening** — egg-install-script compatibility pass, tests, docs.

## Open / parked decisions

- **Penguin git remote/hosting** — undecided; no Penguin `origin` yet, only
  `upstream`.
- **Phase 1 spike Proxmox target** — PARKED. Needs an operator-named node plus
  authorization to create/destroy throwaway LXCs on it (API token pulled from
  Bao/NetBox once named).
- **PDM (Proxmox Datacenter Manager)** — documented target, **PARKED**, not built
  in v1. Its guest-create API surface is unverified; if it can't create guests the
  eventual PDM backend routes to the resolved PVE node API. Keep the `NodeBackend`
  seam so a PDM backend (and/or an in-house PDM provider) slots in later.
- **Deep module/namespace rename** and **user-visible rebrand** — deferred until
  after the Phase 1 technical proof (rebranding an unproven fork is premature).
- **Pre-commit gates** — not yet wired.
