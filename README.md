# Penguin Wings

Wings is Penguin's server control plane, built for the rapidly changing gaming industry and designed to be
highly performant and secure. Wings provides an HTTP API allowing you to interface directly with running server
instances, fetch server logs, generate backups, and control all aspects of the server lifecycle.

In addition, Wings ships with a built-in SFTP server allowing your system to remain free of Penguin specific
dependencies, and allowing users to authenticate with the same credentials they would normally use to access the Panel.

## Documentation

* [Panel Documentation](https://pengwings.dev/docs/panel/getting-started)
* [Wings Documentation](https://pengwings.dev/docs/wings/install)
* Or, get additional help [via GitHub Discussions](https://github.com/JamesonRGrieve/penguin-panel/discussions)

## Proxmox API token

Penguin Wings drives Proxmox over the PVE API with a **dedicated, least-privilege API token** — never the root account. Run this as **root on any node of the cluster** to create a `Penguin` role holding exactly the privileges the LXC lifecycle needs, a token-only `penguin@pve` user, and a token bound to it — it prints the token secret once:

```sh
pveum role add Penguin -privs "VM.Allocate VM.Audit VM.Config.CPU VM.Config.Memory VM.Config.Disk VM.Config.Network VM.Config.Options VM.PowerMgmt Datastore.AllocateSpace Datastore.AllocateTemplate Datastore.Audit Sys.Audit Sys.AccessNetwork SDN.Use" && pveum user add penguin@pve && pveum aclmod / -user penguin@pve -role Penguin && pveum user token add penguin@pve wings --privsep=0
```

Broken out, that is:

```sh
# 1. Least-privilege role — exactly the container-lifecycle privileges Wings uses.
pveum role add Penguin -privs "VM.Allocate VM.Audit VM.Config.CPU VM.Config.Memory VM.Config.Disk VM.Config.Network VM.Config.Options VM.PowerMgmt Datastore.AllocateSpace Datastore.AllocateTemplate Datastore.Audit Sys.Audit Sys.AccessNetwork SDN.Use"

# 2. Dedicated PVE-realm user (token-only; no password needed).
pveum user add penguin@pve

# 3. Grant the role. Applied at / but scoped by the minimal role itself.
pveum aclmod / -user penguin@pve -role Penguin

# 4. Create a revocable token; --privsep=0 makes it inherit the user's role.
pveum user token add penguin@pve wings --privsep=0
```

The final step prints the secret **once**. The full token is `penguin@pve!wings=<secret>` — set it as `proxmox.token` in the Wings config (or the `PROXMOX_VE_API_TOKEN` environment variable). It is injected into the OpenTofu process and the PVE power client at runtime and is never written into generated configuration.

### Why each privilege

| Privilege | Needed for |
|---|---|
| `VM.Allocate` | create / destroy the container |
| `VM.Audit` | read container status and config |
| `VM.Config.CPU` / `.Memory` / `.Disk` / `.Network` / `.Options` | set cores, memory/swap, rootfs, NIC, and container options |
| `VM.PowerMgmt` | start / stop / shutdown |
| `Datastore.AllocateSpace` | allocate the rootfs volume on the target storage |
| `Datastore.AllocateTemplate` | pull the egg's OCI image onto the image storage (`oci-registry-pull`) |
| `Datastore.Audit` | read the storage and its templates (the pulled egg images) |
| `Sys.Audit` | read node status / version |
| `Sys.AccessNetwork` | let the node fetch the egg's OCI image from its registry — **required for the OCI pull**; omit it and `oci-registry-pull` fails with a `403` on `Sys.AccessNetwork` |
| `SDN.Use` | attach the NIC to the bridge — **required on PVE 9**, which models even a plain Linux bridge (`vmbr0`) as an SDN zone. Omit it and container creation fails with a `403` on `/sdn/zones/localnetwork/<bridge>` |

Revoke access by deleting the token (`pveum user token remove penguin@pve wings`); the role and user can be reused for a fresh one.

## Reporting Issues

Feel free to report any wings specific issues or feature requests in [GitHub Issues](https://github.com/JamesonRGrieve/penguin-wings/issues/new).
