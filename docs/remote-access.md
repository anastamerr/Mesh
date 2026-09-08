# Paired remote storage

The backend supports an outbound HTTPS relay connection from both computers. The storage laptop needs no inbound port forwarding. The controller publishes the relay endpoint, so a paired agent and managed storage commands discover it automatically. This is a relay path; automatic LAN discovery, direct peer routing/NAT traversal, service installation, accounts and a GUI remain future work. Remote compute dispatch is not part of this storage transport.

## Deployment and pairing

Apply the database migrations before starting the controller. Set `MESH_RELAY_ORIGIN` to the public HTTPS origin of the relay and `MESH_RELAY_KEY` to a separate random service credential (32–256 URL-safe characters). Keep the operator credential separate. Terminate public controller HTTPS at the deployment edge; apply request rate limits to public pairing creation. Pairing is currently for a single operator's self-hosted deployment, not a multi-tenant account system.

Build `agent/cmd/mesh-agent` and `agent/cmd/mesh-relay`. Start the relay with a valid certificate for its public hostname:

```sh
mesh-relay --listen :7443 --tls-cert relay-cert.pem --tls-key relay-key.pem \
  --auth-url https://controller.example/v1/relay/authorize \
  --auth-service-token-file /private/relay-service-key
```

The service key file contains the same value as controller `MESH_RELAY_KEY`. Restrict access to that file and the TLS private key. The relay listener must be reachable from both computers and must support HTTP/1.1 CONNECT without a proxy buffering the tunnel.

On the spare laptop:

```sh
mesh-agent pair --server https://controller.example --name spare-laptop
```

The agent displays a code and device public-key fingerprint. On the operator computer, use `mesh-agent pair list --server https://controller.example --operator-stdin`, then `mesh-agent pair approve --server https://controller.example --code XXXX-XXXX --fingerprint <verified-fingerprint> --operator-stdin`. Supply the operator credential on stdin, not in process arguments. Compare the fingerprint with the spare laptop before approving.

Run the paired laptop with `mesh-agent run --root <dedicated-storage-folder>`. Use `--state-dir` consistently if a custom identity directory was selected during pairing. The controller and relay addresses are discovered from the saved controller configuration. The process must remain running and the laptop awake; this does not yet install an OS service or change power settings.

The operator uses existing `storage copy`, `storage get` and `storage catalog` commands with `--controller`, `--node` and `--operator-stdin`. `copy` needs `--source`; `get` needs `--collection` and `--destination`. An explicit `--server` keeps the existing direct storage path. `--relay` overrides the discovered relay. `--relay-ca` allows an explicitly selected private CA; ordinary public certificates require no custom CA.

## Trust and recovery guarantees

- The device saves its private key and pending pairing secrets before contacting the controller. Windows uses the existing DPAPI protection; Unix uses private files. Retrying after process death reuses the pending identity. Approval is idempotent and requires the expected key fingerprint.
- Inside the relay tunnel, the consumer establishes TLS 1.3 to the device and verifies its paired SHA-256 SPKI fingerprint and certificate validity. Storage grants travel inside this connection. The relay cannot decrypt storage content or receive the device's controller credential.
- The controller issues separate, role-bound relay tickets with at most ten minutes of validity. A ticket only permits routing to one node; it cannot authorize storage operations, renew a device, or mint another ticket. The relay service credential only authorizes relay lease checks, not operator actions.
- The relay bounds waiting and active connections, rechecks authorization every fifteen seconds, and closes streams when authorization expires, is revoked, or cannot be revalidated. Interrupted retrievals retain verified progress and reconnect with fresh authorization. A malicious relay can still disrupt availability or observe timing and byte counts.
- Paired running devices extend their existing credential lease to thirty days when fewer than seven days remain. This is lease renewal, not credential rotation. Expired or revoked credentials cannot renew; a laptop offline beyond expiry needs explicit identity recovery/re-enrollment.
- Managed retrieval persists checksummed checkpoints every 4 MiB and verifies saved bytes before requesting the remaining range. Complete small files are verified and reused. Process termination, relay restart and local partial-file corruption are recoverable. The final collection is checksum-verified and published without replacing an occupied destination.
- Recovery files live beside the destination: `.<name>.mesh-download`, a staging directory named in that journal, and `.<name>.mesh-download.lock`. Re-run the same retrieval with the same destination to resume. The lock file may remain after success; it is not downloaded content. Do not move or edit staging files while a retrieval runs.

The protocol uses standard [HTTP CONNECT semantics](https://www.rfc-editor.org/rfc/rfc9110.html#name-connect) and Go's [TLS verification hooks](https://pkg.go.dev/crypto/tls#Config). Direct peer routing can later avoid relay bandwidth without changing the storage protocol or the device identity pin.

## Validation

Build both native executables into `agent/bin`, run `npm run build`, set `MESH_TEST_DATABASE_URL` to a disposable PostgreSQL database, and run `npm run test:remote`. `MESH_GO_BINARY` optionally selects Go for the temporary localhost certificate fixture. The scenario creates an isolated schema and runs actual controller, agent and TLS relay processes; it retains a report and fixtures under the OS temporary directory and removes its service/TLS private key files.

The Windows loopback run on 2026-09-08 passed pairing restart, native CLI approval, repeated approval, renewal, relay credential isolation, a 128 MiB upload, killed retrieval and relay restart, incorrect key rejection, corrupt checkpoint recovery, destination protection, 200 small-file hash comparisons and revocation. One interrupted download reused 8 MiB (6.25% of the payload) instead of restarting from zero. The final upload took 2.387 s and resumed retrieval took 2.030 s on this host; [raw report](testing/measurements/remote-loopback-2026-09-08.json). These are local process measurements, not WAN throughput claims or a before/after speed benchmark. A separate real TCP/TLS regression scenario sustains one-way traffic for approximately 600 ms with a 100 ms idle timeout, checking that an inactive reverse direction cannot interrupt an active transfer.

Public deployment and separate physical networks are still required to validate real-device WAN behavior, sleep/wake, restricted networks, bandwidth and latency. No public relay or second remote device was supplied for that validation.
