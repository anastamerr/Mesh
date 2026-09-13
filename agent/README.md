# Native components

The Go agent can enroll, persist an identity, collect CPU/RAM inventory, send authenticated heartbeats, serve native storage, provision its dedicated WSL2 environment, reconcile typed one-container workloads, and run under the Windows Service Control Manager.

## Guided setup

The `mesh-windows-amd64` artifact contains `mesh-agent.exe`, the compute tar and checksum, and `setup-mesh.ps1`. The preferred host setup is:

```powershell
.\setup-mesh.ps1 -Server https://mesh.example -StorageRoot C:\Mesh\storage -EnableCompute
```

Its equivalent direct, automation-friendly command is:

```powershell
.\mesh-agent.exe setup --server https://mesh.example --name "Spare laptop" `
  --state-dir C:\Mesh\state --root C:\Mesh\storage `
  --compute-bundle .\mesh-wsl-rootfs-amd64.tar
```

It starts pairing, waits for operator approval, initializes the dedicated native storage root, verifies/imports compute when requested, and prints the exact foreground run command. Identity state and contributed storage are required to be separate absolute directories. Existing matching identity and ready compute stages are reused; conflicting identity or WSL state fails closed.

For automatic startup, run the setup command elevated with `--install-service`, `--account`, and `--password-stdin`. See [ADR 0011](../docs/decisions/0011-guided-setup.md).

With Go 1.25 or newer, from this directory:

```sh
go run ./cmd/mesh-agent info
go build -o bin/mesh-agent ./cmd/mesh-agent
go run ./cmd/mesh-executor version
go vet ./...
go test ./...
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o bin/mesh-agent.exe ./cmd/mesh-agent
```

Download the CI/release `mesh-wsl-rootfs-amd64` artifact, keep its tar and `.sha256` sidecar together, and provision the dedicated environment without modifying other distributions:

```powershell
mesh-agent compute setup --bundle .\mesh-wsl-rootfs-amd64.tar
mesh-agent compute doctor
```

If Windows has not enabled WSL2—or Mesh is running under a different Windows account—setup stops without touching Mesh state and explains the ownership prerequisite. Enabling WSL can require an elevated `wsl --install --no-distribution` and a restart. The bundle digest is mandatory; an existing unhealthy distribution is never overwritten. See [ADR 0008](../docs/decisions/0008-wsl-provisioning.md).

Then run storage and compute together on Windows:

```sh
mesh-agent run --root C:\Mesh\storage --compute --wsl-distribution Mesh
```

The agent converts only approved collection and private job-output paths into WSL form and sends a bounded typed request to the executor. A successful job's `/mesh/data` volume is imported into the native store, checksum verified, and reported as a retrievable output collection before the job becomes terminal. See [the compute reconciliation contract](../docs/decisions/0007-compute-reconciliation.md). Do not expose the Docker socket outside that WSL environment.

On the development Mac's conda-provided Go toolchain, set `CC=/usr/bin/clang` for commands that use cgo (including race tests).

## Connect to the local API

Start PostgreSQL and `npm run dev` as described in the root README. Build the native agent above. From the project root, use the development-only helper:

```sh
npm run agent:enroll -- --name "Development Mac"
agent/bin/mesh-agent status
agent/bin/mesh-agent heartbeat
agent/bin/mesh-agent run --root "$HOME/mesh-files"
```

The helper reads the ignored backend `.env`, requests a one-use token from the loopback API, and delivers it over stdin. It does not pass the operator key or database credentials to the agent. To enroll on a separate host, supply an operator-issued token over stdin to `mesh-agent enroll --server https://your-control-plane --name Lenovo --token-stdin`. Do not put secrets in command arguments or shell history. Remote use still requires properly configured HTTPS and the backend's planned authentication hardening.

Every stateful command accepts `--state-dir` for isolated identities. The default is `Mesh/agent` under the current user's OS configuration directory. `run` sends immediately, then every 15 seconds, reconnects with bounded jittered backoff, and exits on Ctrl+C or permanent authentication/contract errors. `--interval` accepts 1s to 30s for development.

## Identity and persistence guarantees

- One process holds the state-directory lock; stop `run` before issuing another command against that identity.
- Unix state uses a 0700 directory and 0600 file. It is not encrypted. An explicitly supplied existing directory must already be private.
- Windows state is encrypted using current-user DPAPI, without machine-wide scope. Service installation enforces the same current account; moving state between users is not supported.
- State is atomically replaced after syncing the temporary file. The next heartbeat sequence is saved before sending. Lost responses create gaps, not duplicate sequence numbers.
- Corrupt state and existing identities are never silently reset or overwritten. Restoring an older state copy can cause sequence conflicts; investigate and re-enroll instead of resetting the counter.
- Expired/revoked credentials stop the runner. Rotation is not implemented. To replace an identity, revoke it on the controller and use a new state directory; preserve old state for diagnosis.
- An enrollment response lost after controller commit can leave an orphaned node. Inspect/revoke it before retrying.

Tests cover these contracts; Windows DPAPI/service behavior still needs actual Windows validation. CI includes Windows, macOS, and Linux tests. The module name is intentionally local until the repository's public location is decided.

With `--root`, `run` also serves enrolled storage and shuts both tasks down together. With `--compute` on a paired relay-connected node, it additionally maintains authenticated routes for desired-running applications. Without it, `run` remains heartbeat-only. See [ADR 0006](../docs/decisions/0006-transfer-efficiency.md) for batching, retries, and setup direction.

Operators connect to an observed-running application without exposing it publicly:

```sh
mesh-agent workload connect --controller https://controller.example --operator-stdin \
  --id APPLICATION_UUID --listen 127.0.0.1:8080
```

The operator credential is read from stdin. The local listener is loopback-only, relay tickets are scoped to that application UUID, and the stream verifies the public key approved during device pairing. See [ADR 0009](../docs/decisions/0009-authenticated-application-routing.md).

## Windows service

First enroll, pair, and validate `run --root ... --compute` in the foreground. Then use an elevated PowerShell terminal to install the same binary under the exact current Windows user. Explicit absolute state and storage paths prevent service working-directory or profile ambiguity:

```powershell
$credential = Get-Credential $env:USERDOMAIN\$env:USERNAME
$credential.GetNetworkCredential().Password | .\mesh-agent.exe service install `
  --account "$env:USERDOMAIN\$env:USERNAME" --password-stdin `
  --state-dir C:\Mesh\state --root C:\Mesh\storage --compute
```

The password is read once from stdin and handed to the Service Control Manager; Mesh does not persist it itself. Installation rejects another account because the node credential and paired key are protected with current-user DPAPI. The service starts automatically with delayed startup and bounded recovery restarts. Logs go to the Windows Application event log under `MeshPersonalCloud`.

Use `mesh-agent service status`, `stop`, `start`, and `uninstall` from an elevated terminal. Uninstall stops the runner before removing the service registration and event source; it preserves identity, storage, WSL, containers, and volumes. See [ADR 0010](../docs/decisions/0010-windows-service.md).

Next: live Windows reboot/logout/sleep, WSL/Docker, and separate-network WAN validation.

## Copy folders with native storage

Build `bin/mesh-agent` as above. From the `agent` directory, use a directory outside the repository for private state. On Unix:

```sh
mkdir -p "$HOME/.mesh-local"
chmod 700 "$HOME/.mesh-local"
bin/mesh-agent storage keygen --key-file "$HOME/.mesh-local/storage.key"
bin/mesh-agent storage serve --root "$HOME/.mesh-local/files" --key-file "$HOME/.mesh-local/storage.key"
```

In another terminal:

```sh
bin/mesh-agent storage upload --server http://127.0.0.1:7332 --key-file "$HOME/.mesh-local/storage.key" --source /absolute/path/to/folder
bin/mesh-agent storage list --server http://127.0.0.1:7332 --key-file "$HOME/.mesh-local/storage.key"
bin/mesh-agent storage download --server http://127.0.0.1:7332 --key-file "$HOME/.mesh-local/storage.key" --id COLLECTION_ID --destination /absolute/path/to/new-copy
```

Upload prints the collection ID. Stop and restart the storage server, then repeat the same upload command to resume. Completed files live under `<root>/collections/<id>/` as ordinary files. `list --after COLLECTION_ID` retrieves the next page after a 100-item page. Downloads require a destination that does not exist.

For controller-authorized storage, use `storage serve --enrolled --root <directory>` and obtain separate read/write/list grants. See [the complete enrollment and transfer workflow](../docs/decisions/0004-storage-authorization.md). `storage identify --source <folder>` prints the collection ID needed for a write grant.

The key-based demo above is restricted to loopback. For another machine, use enrolled serving with `--listen <address>:7332 --tls-cert <certificate.pem> --tls-key <private-key.pem>` and a client HTTPS URL matching a trusted certificate. There is no certificate-verification bypass. Windows uses the same commands; directory/key ACL provisioning and runtime validation remain pending.

Enrolled serving validates each request with the controller and honors grant expiry and node revocation on subsequent requests. Controller outages fail closed. Requests already authorized may finish. The local shared-key mode remains independent of controller authorization.

## Managed transfers

From the repository root, `npm run mesh -- copy|catalog|get ...` handles collection IDs and scoped grants automatically. See [ADR 0005](../docs/decisions/0005-collection-workflow.md) for commands, progress, resumption and catalogue semantics. The underlying `mesh-agent storage copy|catalog|get` commands accept an operator key only over stdin with `--operator-stdin`; the development helper supplies it without placing it in arguments or child environment.
