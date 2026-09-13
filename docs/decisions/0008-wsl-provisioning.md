# ADR 0008: Verified WSL2 compute bundle provisioning

Status: implemented and unit tested; real Windows import, reboot, sleep, and upgrade validation remain pending.

## Decision

Mesh distributes compute as an importable WSL2 root filesystem rather than modifying an arbitrary user distribution. The bundle contains Ubuntu, Docker Engine configured for its local Unix socket only, systemd enablement, and `mesh-executor`. CI builds the bundle from `deploy/wsl/Dockerfile`, exports it as a tar file, records its SHA-256 digest, and retains both as one artifact.

On Windows, `mesh-agent compute setup` verifies the entire bundle against an explicit digest or adjacent `.sha256` file before invoking `wsl --import ... --version 2`. The distribution and virtual-disk location are dedicated to Mesh and scoped to the current Windows user. Setup never changes or unregisters a distribution that existed before the command. If a newly imported environment cannot make Docker ready after bounded retries, only that newly created distribution and install directory are rolled back.

`mesh-agent compute doctor` checks WSL presence, exact distribution presence, executor response shape, architecture, Docker runtime, and version. Agent reconciliation invokes `mesh-executor` explicitly as root inside that distribution. Docker has no TCP listener, and neither the controller nor native agent exposes its Unix socket.

## User flow

Download the matching `mesh-wsl-rootfs-<architecture>` artifact and keep its tar and `.sha256` files together. Then run:

```powershell
mesh-agent compute setup --bundle .\mesh-wsl-rootfs-amd64.tar
mesh-agent compute doctor
mesh-agent run --root C:\Mesh\storage --compute
```

If WSL itself is unavailable, setup stops without modifying storage or identity state and directs the user to enable WSL2 or use the Windows account that owns the distributions. Enabling Windows features uses the standard elevated `wsl --install --no-distribution` command and may require a restart. Repeating setup against an already-ready `Mesh` distribution is safe and returns its readiness rather than re-importing it.

## Boundaries and remaining validation

- The first retained artifact is amd64. An arm64 artifact and release-signing pipeline are required before claiming arm64 onboarding support.
- SHA-256 prevents accidental or substituted bundle installation only when the digest is obtained with the trusted release. Release signing remains required for public distribution.
- Setup does not silently repair or replace an existing unhealthy distribution because it may contain persistent application volumes. Upgrade, repair, and removal need explicit data-preservation semantics.
- Windows WSL availability, systemd/Docker boot, logout, reboot, sleep/resume, disk growth, and current-user ownership still require validation on the reference laptop.
