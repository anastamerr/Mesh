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

Next: controller-authorized storage routing, Windows service lifecycle validation, then a separately authenticated WSL executor.

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

For another machine, securely provision the same storage key on the client and configure `serve --listen <address>:7332 --tls-cert <certificate.pem> --tls-key <private-key.pem>`. Use an HTTPS URL whose hostname matches a certificate trusted by the client. There is no certificate-verification bypass. Windows uses the same agent commands; provision private directory/key ACLs for the serving account.

This is a direct storage connection, separate from enrollment/heartbeats. A node's control-plane revocation does not yet disable its storage key. Keep the listener on loopback for the local demo; controller-issued permissions and gateway routing are the next integration step. Read [the storage contract and limitations](../docs/decisions/0003-native-storage.md) before using it across machines.
