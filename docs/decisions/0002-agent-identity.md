# ADR 0002: Foreground agent identity and heartbeat persistence

Status: implemented; Windows runtime validation pending.

The first working agent uses the existing versioned HTTP API. A future control stream can reuse its identity and sequence rules. It is a foreground command, not a Windows service, so account ownership remains explicit.

## State

One private state directory represents one node identity. A cross-process OS lock excludes simultaneous runners, enrollment, and state inspection. A small atomically replaced state document is sufficient for identity and one sequence counter. This is not a general operations database: SQLite remains the planned store for transfer/workload journals when those features exist.

On Unix, state is plaintext within a 0700 directory and a 0600 file. On Windows, DPAPI encrypts the document for the current user. Do not switch a future service to LocalSystem and assume it can decrypt the interactive user's identity. Re-enrollment or an explicit identity migration will be needed.

Reserve and persist the next heartbeat sequence before sending. Lost responses create harmless gaps. Never reset the counter on restart, corruption, expiry, or conflict; a missing or null counter is corruption, not zero. Atomic file replacement plus file synchronization reduces partial-write risk; this is not a guarantee against storage-controller failure or restoring old backups of agent state.

## Transport and failure

Require HTTPS except loopback development origins. Reject URL userinfo, paths, queries, and fragments. Disable redirects and do not log response bodies. Each request has a timeout and a bounded response body. A successful heartbeat must include a valid accepted acknowledgement; a 2xx status alone is insufficient. Bounded error bodies are drained for connection reuse.

Retry temporary connection/server failures with exponential jittered backoff. Stop on expired/revoked identity, contract errors, sequence exhaustion, or failed local persistence. Ctrl+C cancels network work and waits. Inventory collection failures retry without inventing readings.

The enrollment token is accepted only on stdin. The local helper obtains it using the backend operator key, then strips operator/database credentials from the child agent environment. The helper is development-only and always targets loopback.

## Validation

Go tests cover state exclusion/reopen, corruption, credential-safe output, transport restrictions, and sequence behavior. An optional backend test runs the compiled agent against an isolated PostgreSQL schema and a real HTTP listener, including controller restart and revocation. CI additionally targets Windows, macOS, and Linux; local cross-compilation does not replace Windows runtime testing.
