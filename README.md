# Mesh

**Your hardware. One cloud.** Install Mesh on spare hardware to store files and run applications remotely.

Start with [the product idea and roadmap](PROJECT.md), [architecture decision](docs/decisions/0001-foundation.md), and [HTTP contract](contracts/README.md).

Follow [the engineering standards](docs/engineering-standards.md) for modularity, performance claims, and pre-commit verification.

For a self-hosted public controller and relay, use the [cloud deployment](deploy/cloud/README.md). It keeps PostgreSQL private, mounts bounded secret files, applies migrations before startup, and exposes the native TLS relay for storage and application routes.

## Current status

Backend-complete single-owner MVP and native Windows agent. Implemented: PostgreSQL migrations, operator-authorized enrollment token creation, atomic one-use node enrollment, hashed expiring node credentials, validated heartbeats, observed presence, listing, revocation, and persisted lifecycle audit events.

The Go agent enrolls, stores its identity, reports real CPU/RAM inventory, and sends heartbeats with durable sequences and reconnect backoff. Native storage supports authenticated folder uploads and resumable retrieval, checksum verification and listing. Paired devices can use an outbound HTTPS relay with end-to-end device-key verification; see [remote setup, trust and validation](docs/remote-access.md). Enrolled storage validates short-lived, node/collection-scoped permissions with the controller on every request; shared storage keys are loopback-only development mode. The controller, agent and Linux executor implement typed one-container workload reconciliation with digest-pinned images, limits, stable volumes and read-only collection inputs. Successful job volumes are exported into checksum-verified native collections for ordinary retrieval. A verified WSL2 bundle provides one-command compute provisioning without changing other distributions. Applications use workload-scoped relay routes with paired-device end-to-end TLS and a loopback-only operator connection command. The Windows agent can install as an auto-start service under the enrolled user without changing DPAPI scope. Real public-WAN, WSL/Docker, and reboot/sleep validation still require the target hardware; multi-user accounts and UI are intentionally outside this backend phase. See [ADR 0007](docs/decisions/0007-compute-reconciliation.md), [ADR 0008](docs/decisions/0008-wsl-provisioning.md), [ADR 0010](docs/decisions/0010-windows-service.md), and [agent commands and guarantees](agent/README.md).

## Guided backend setup

Download and extract the CI/release `mesh-windows-amd64` artifact on the spare laptop. Its wrapper creates the paired identity, initializes a separate storage root, optionally provisions the verified WSL compute bundle, and can install unattended Windows hosting:

```powershell
.\setup-mesh.ps1 -Server https://mesh.example -StorageRoot C:\Mesh\storage -EnableCompute
```

The equivalent direct command is:

```powershell
.\mesh-agent.exe setup --server https://mesh.example --name "Spare laptop" `
  --state-dir C:\Mesh\state --root C:\Mesh\storage `
  --compute-bundle .\mesh-wsl-rootfs-amd64.tar
```

Confirm the displayed code and device-key fingerprint from the operator machine. Rerunning the same command safely continues from the completed identity or compute stages rather than replacing them. Add `--install-service --account "$env:USERDOMAIN\$env:USERNAME" --password-stdin` from an elevated terminal to finish with automatic startup; the password is consumed only by the service-install stage.

Use `-InstallService` from an elevated PowerShell session for an interactive, history-safe account prompt. See [ADR 0011](docs/decisions/0011-guided-setup.md) for resumability and directory-isolation guarantees. The individual commands below remain available for diagnosis and automation.

## Run an enrolled node

```sh
mesh-agent run --root /absolute/path/to/mesh-files
```

This runs heartbeats and storage together. Paired devices discover the controller's configured relay and connect outbound. Direct remote listening requires `--listen`, `--tls-cert`, and `--tls-key`. It remains a foreground process. See [transfer efficiency and setup direction](docs/decisions/0006-transfer-efficiency.md).

On Windows, download the matching WSL artifact, keep its tar and `.sha256` sidecar together, and run `mesh-agent compute setup --bundle .\mesh-wsl-rootfs-amd64.tar`. `mesh-agent compute doctor` gives a concise readiness result. Then add `--compute` to reconcile assigned workloads through the dedicated `Mesh` distribution.

Once that execution environment reports ready, the backend-only operator flow is:

```sh
npm run mesh -- job --node Lenovo --name "Transcode" \
  --image registry.example/transcoder@sha256:FULL_DIGEST \
  --input COLLECTION_ID --cpu 2000 --memory-mib 512 \
  --arg convert --arg /mesh/input/video.mp4 --arg /mesh/data/output.mp4
npm run mesh -- workloads
npm run mesh -- readiness --node Lenovo
```

Use `app` with `--port`, then `stop --id WORKLOAD_ID` and `start --id WORKLOAD_ID` for long-running applications. Arguments are repeated typed values, not a remotely interpreted shell command.

After the application is observed running, open a private local route for a browser or TCP client:

```sh
npm run mesh -- connect --id WORKLOAD_ID --listen 127.0.0.1:8080
```

The listener rejects non-loopback addresses. Each connection obtains a short-lived workload ticket and verifies the paired device key through the opaque relay; Docker never publishes the container port on the host.

After validating the foreground flow, install unattended startup from an elevated PowerShell session. The account must be the same Windows user that enrolled the node; pipe its password over stdin rather than putting it in command history:

```powershell
$credential = Get-Credential $env:USERDOMAIN\$env:USERNAME
$credential.GetNetworkCredential().Password | .\mesh-agent.exe service install `
  --account "$env:USERDOMAIN\$env:USERNAME" --password-stdin `
  --state-dir C:\Mesh\state --root C:\Mesh\storage --compute
```

Use `mesh-agent service status|stop|start|uninstall` for lifecycle management. See [ADR 0010](docs/decisions/0010-windows-service.md) for account and validation constraints.

When a job reaches `succeeded`, the `OUTPUT` column contains its immutable collection ID. Retrieve it through the existing storage path:

```powershell
npm run mesh -- get --node Lenovo --collection <output-collection-id> --destination C:\Mesh\results\transcoded
```

## Copy and retrieve folders

After enrollment and enrolled storage startup:

```sh
npm run mesh -- copy --node Lenovo --source /absolute/path/to/Photos
npm run mesh -- catalog --node Lenovo
npm run mesh -- get --node Lenovo --collection Photos --destination /absolute/path/to/RestoredPhotos
```

The managed commands obtain grants in memory, show progress, and resume copies when rerun. See [the catalogue workflow and guarantees](docs/decisions/0005-collection-workflow.md). `npm run test:live` runs the real-process experience check against an isolated schema in the configured test database.

## Local setup

For the user-local Go/PostgreSQL installation on the development Mac, see [local development instructions](docs/local-development.md). That installation can be used instead of the Compose database below.

Prerequisites: Node.js 22.18+, npm, Docker Compose (or a PostgreSQL 17 database). Go 1.25+ is needed for the native agent.

```sh
npm install
docker compose -f deploy/compose.yaml up -d
cp apps/control-plane/.env.example apps/control-plane/.env
```

Replace `MESH_ADMIN_KEY` in that `.env` with a random value. Generate one using:

```sh
node -e "console.log(require('node:crypto').randomBytes(32).toString('base64url'))"
```

Then:

```sh
npm run db:migrate
npm run dev
```

The API binds to `127.0.0.1:3000`. Visit `/health/ready` to verify the database schema. Root npm workspace scripts run with the control-plane directory as working directory, where `.env` is loaded. For compiled startup, run `npm run start --workspace @mesh/control-plane` after building.

This loopback Compose environment and its password are for local development only. For an Internet-facing, single-owner deployment, use the TLS-terminated stack in [`deploy/cloud`](deploy/cloud/README.md). Multi-user accounts, automatic key rotation, and request-rate policies remain outside the current single-owner scope.

## Verify

```sh
npm run lint
npm run typecheck
npm test
npm run build
```

Tests use an in-memory repository for HTTP/security behavior; they do not validate PostgreSQL locking. Optional PostgreSQL integration tests are described in the test source and enabled with `MESH_TEST_DATABASE_URL` pointing to a dedicated test database. Never use a production database for tests.

Set `MESH_TEST_AGENT_BINARY` to the absolute path of a compiled agent to also run the real-agent integration test against an isolated PostgreSQL schema and temporary HTTP server. It verifies enrollment, sequence persistence across agent processes and controller restart, and revocation.

CI provisions PostgreSQL, runs migrations twice, builds the agent, and runs the complete integration suite. Native jobs check Go formatting, vet, and race tests on Linux, macOS, and Windows. These checks execute when the repository is pushed to GitHub; adding the workflow does not mean it has already run.

See [agent instructions](agent/README.md) for native commands. No frontend workspace has been created.
