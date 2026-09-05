# Mesh

**Your hardware. One cloud.**

## Product idea

Mesh makes spare computers useful from other devices. Install it on an existing Windows laptop, choose storage to contribute, upload folders, and optionally enable Linux-based applications and jobs that use that computer's CPU, RAM, and storage. Users keep their existing operating system.

The initial reference machine is an old Lenovo with an i7 CPU, 16 GB RAM, and 1 TB storage. Mesh is currently a project for fun, learning, and an impressive real demonstration—not a revenue-first product.

Storage and compute are equally important. A user may use Mesh only for file copies; those copies must remain accessible without a working Linux runtime. Resources form an allocation pool, not one machine with combined RAM or CPU performance.

## First experience

1. Install and enroll the Windows agent.
2. See actual hardware and connection status in a future browser dashboard.
3. Choose a dedicated storage directory and transfer a folder to it.
4. Resume an interrupted upload and verify the result.
5. Enable a dedicated WSL2 environment and run a containerized job against selected files.
6. Deploy a long-running application and access it through an authenticated remote route.

The demo: upload a video, convert it on the Lenovo, retrieve the output, then host a small application without configuring home-router forwarding.

## Scope and non-goals

The first supported host is Windows; any device with a browser can eventually manage it. macOS and native Linux hosts follow. No UI implementation is being built in this initial backend phase.

Initially support explicit placement, one owner, native file collections, one-container applications, and run-to-completion jobs. Do not promise high availability, hard disk quotas, continuous synchronization, automatic failover, or desktop application streaming.

Defer multi-user organizations, public publishing, arbitrary shell access, Compose compatibility, build pipelines, GPU provisioning, distributed filesystems, and automatic scheduling. Do not introduce Kubernetes or custom NAT traversal simply to demonstrate complexity.

## Technology decisions

- **Go** for the native agent and Linux executor; this is a long-term choice, not a temporary implementation to rewrite in Rust.
- **TypeScript, NestJS, Fastify** for one modular control-plane service.
- **PostgreSQL** for central identities, desired state, operations, and metadata.
- **SQLite** later for agent-local durable transfer and operation state.
- **WSL2 and Docker Engine** for the first Windows compute environment; validate lifecycle before committing to unattended startup guarantees.
- **React and TypeScript** for a later browser dashboard.
- **Resumable HTTP uploads** for files; ordinary host files remain independently readable.
- **Caddy and frp** are candidates for the initial remote gateway, pending integration and authorization validation. Later add direct LAN and established peer networking.

## Architectural invariants

1. Separate the physical node from its execution environments; do not double-count WSL resources.
2. Store uploaded files on the host. Keep Linux application volumes inside the Linux filesystem.
3. Give nodes, workloads, collections, volumes, and operations stable identities independent of Docker IDs or filesystem paths.
4. The controller persists desired state; agents report observed state and reconcile. Retries must not duplicate work.
5. Keep bulk data separate from control messages and central metadata storage.
6. Limit file access to approved roots and execution to typed operations. No remotely exposed Docker socket or unrestricted host shell.
7. Distinguish unreachable, unhealthy, unknown, and stopped. An unreachable node may still be running.
8. Preserve persistent data when deleting an application; data destruction is separate.
9. Model capabilities and architecture explicitly. GPU presence does not imply workload compatibility.
10. Never describe a copy as a backup or replica unless its actual semantics justify that claim.

## Expansion roadmap

| Stage | Deliverable | Exit evidence |
| --- | --- | --- |
| Foundation | Enrollment, identity, inventory, heartbeat, revocation | Invalid/replayed credentials rejected; stale presence visible |
| File storage | Approved directories, resumable folder transfers, integrity validation | Interrupted transfer recovers; paths cannot escape root |
| Compute | Linux executor, container jobs and applications, resource limits | Job output verified; apps stop/start without losing volumes |
| Remote access | Authenticated application routes and transfer gateway | Works across separate networks; unauthorized paths blocked |
| Reliable operation | Reboot/reconnect recovery, bounded logs, disk alerts | Real Windows reboot/logout/sleep tests pass |
| Protection | Versioned independent backups and restore | Restore into a new volume succeeds, including application validation |
| Multiple nodes | Explicit placement and cross-node copies | Ownership and resource accounting remain correct offline |
| Placement | Capability, resource, availability, and storage-aware decisions | Explainable decisions with no unnecessary movement |
| Relocation | Stop, copy, verify, start, switch route | Measured downtime and intact data |
| Selective failover | Eligible workloads recover elsewhere | Writer fencing and recovery guarantees established |

Replication is not backup. Relocation is not live migration. A container image rollback is not a database rollback. Stateful failover requires more than a heartbeat timeout.

## Current implementation checkpoint

See README.md for runnable components and limitations. Update this section and architecture decisions when scope changes. The first slice is backend-only enrollment and heartbeats; the Go commands are skeletons, not working Windows services.

## Open validation questions

- Exact Lenovo model, Windows version, and virtualization support.
- WSL account ownership, boot before login, logout, sleep/resume, and disk behavior.
- Reachable gateway host and domain for the first remote demo.
- Independent backup destination when protection is implemented.
- Supported hard quota mechanism before claiming enforcement.

## Working rule

Build useful vertical slices. Keep security and data correctness from the beginning; add distributed coordination only when a demonstrated use case needs it.
