# Native components

The Go agent can enroll, persist an identity, collect CPU/RAM inventory, and send authenticated heartbeats. It is a foreground process, not yet an installed Windows service. The executor remains a skeleton.

With Go 1.25 or newer, from this directory:

```sh
go run ./cmd/mesh-agent info
go build -o bin/mesh-agent ./cmd/mesh-agent
go run ./cmd/mesh-executor version
go vet ./...
go test ./...
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o bin/mesh-agent.exe ./cmd/mesh-agent
```

On the development Mac's conda-provided Go toolchain, set `CC=/usr/bin/clang` for commands that use cgo (including race tests).

## Connect to the local API

Start PostgreSQL and `npm run dev` as described in the root README. Build the native agent above. From the project root, use the development-only helper:

```sh
npm run agent:enroll -- --name "Development Mac"
agent/bin/mesh-agent status
agent/bin/mesh-agent heartbeat
agent/bin/mesh-agent run
```

The helper reads the ignored backend `.env`, requests a one-use token from the loopback API, and delivers it over stdin. It does not pass the operator key or database credentials to the agent. To enroll on a separate host, supply an operator-issued token over stdin to `mesh-agent enroll --server https://your-control-plane --name Lenovo --token-stdin`. Do not put secrets in command arguments or shell history. Remote use still requires properly configured HTTPS and the backend's planned authentication hardening.

Every stateful command accepts `--state-dir` for isolated identities. The default is `Mesh/agent` under the current user's OS configuration directory. `run` sends immediately, then every 15 seconds, reconnects with bounded jittered backoff, and exits on Ctrl+C or permanent authentication/contract errors. `--interval` accepts 1s to 30s for development.

## Identity and persistence guarantees

- One process holds the state-directory lock; stop `run` before issuing another command against that identity.
- Unix state uses a 0700 directory and 0600 file. It is not encrypted. An explicitly supplied existing directory must already be private.
- Windows state is encrypted using current-user DPAPI, without machine-wide scope. The future Windows service must use the same account or explicitly re-enroll; moving its state between users is not supported.
- State is atomically replaced after syncing the temporary file. The next heartbeat sequence is saved before sending. Lost responses create gaps, not duplicate sequence numbers.
- Corrupt state and existing identities are never silently reset or overwritten. Restoring an older state copy can cause sequence conflicts; investigate and re-enroll instead of resetting the counter.
- Expired/revoked credentials stop the runner. Rotation is not implemented. To replace an identity, revoke it on the controller and use a new state directory; preserve old state for diagnosis.
- An enrollment response lost after controller commit can leave an orphaned node. Inspect/revoke it before retrying.

Tests cover these contracts; Windows DPAPI/service behavior still needs actual Windows validation. CI includes Windows, macOS, and Linux tests. The module name is intentionally local until the repository's public location is decided.

Next: Windows service lifecycle validation, storage-root permissions, resumable transfers, then a separately authenticated WSL executor.
