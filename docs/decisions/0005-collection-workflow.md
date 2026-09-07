# ADR 0005: Collection catalogue and managed CLI transfers

Status: implemented. No UI or remote gateway is added in this checkpoint. [ADR 0006](0006-transfer-efficiency.md) updates batching, automatic transient-error retries, and the foreground node command.

## User workflow

After enrollment, controller startup and enrolled storage startup (ADR 0004), the development operator can work by node and collection name:

```sh
npm run mesh -- copy --node Lenovo --source /absolute/path/to/Photos
npm run mesh -- catalog --node Lenovo
npm run mesh -- get --node Lenovo --collection Photos --destination /absolute/path/to/RestoredPhotos
```

The local helper reads the existing controller `.env`, supplies the operator key to the CLI over stdin, and strips controller/database secrets from the child's environment. No manual collection-ID step or grant files are needed. It defaults to the loopback storage listener on port 7332; supply `--server https://your-agent:7332` for an enrolled TLS listener elsewhere. This is still direct connectivity, not NAT traversal.

`copy --name <name>` chooses a catalogue name; otherwise the source directory's name is used. `get --collection` accepts a name or full collection ID. An exact ID takes precedence over display names and stops catalogue pagination once found. Duplicate names produce an actionable error asking for the full ID. `catalog` includes full IDs and reads every page, 100 entries per request. Node names are resolved from the existing recent-node list; use a full UUID for a node outside its 100-entry window or with an ambiguous name.

Progress goes to stderr and results to stdout. Feedback begins with connection/scanning phases, then displays bytes, percentage and reused bytes at most once per second. Verification is a separate phase; reaching 100% uploaded bytes does not mean finalization is finished. `--json` emits a copy/get result or newline-delimited catalogue entries for scripts. Empty catalogues explain how to add a copy.

The CLI holds the source root open while it scans once, registers metadata and requests a grant, then uploads from the same prepared manifest. Final file checksums still protect against source changes. A subsequent invocation scans again; there is no persistent scan cache or filesystem snapshot guarantee. The existing low-level storage commands remain available.

## Catalogue semantics

PostgreSQL stores one row per `(node_id, collection_id)` containing name, file count, total bytes and the time of the latest agent confirmation. The operator may register or rename a collection, but cannot mark it confirmed. File bytes and full manifests remain on the agent.

- Registration creates a **pending** row. Repeating registration preserves any existing confirmation.
- After local publication and verification, the enrolled agent reports statistics using its node credential. Only that unrevoked node with an unexpired credential can confirm its copy. Actual reported statistics replace the requested statistics.
- Copies made through low-level grants can also be reported; their default catalogue name is the full ID until an operator supplies a name.
- If the controller misses the confirmation, storage finalization returns an error even though the verified native files may already exist. Repeating `copy` finds the complete local copy and retries confirmation without retransmitting its bytes.
- `get` refuses a pending entry and explains how to finish/reconcile it.

“Confirmed” means the agent reported successful publication at `confirmedAt`. It does not prove current node availability, continuously verify files, or imply backup/replication. Revocation preserves catalogue history and native files. Already-authorized operations retain the guarantees and limits from ADR 0004.

## Authorization and retries

The managed CLI obtains a collection-scoped read or write grant in memory. On a 401, it asks the controller for a new grant and replays that request once, including its chunk body. Revocation or expired node identity prevents renewal. There is no retry loop that bypasses controller policy and no cached authorization decision.

Connection failures stop with instructions to rerun the copy command; durable offsets support resumption. Downloads still require a new destination and remove partial output on ordinary failures. Abruptly killing a downloader may leave a partial folder that must be removed or replaced before retrying.

## Measured efficiency work

Live exercise exposed repeated manifest decoding on the small-file path. The agent now retains only the most recently read immutable manifest, bounded by the existing manifest limit. Publication state is queried every time and authorization is still validated per request. Switching collections replaces the cache entry; no unbounded catalogue cache was added.

Nonempty files are synced before their completed offsets commit, so finalization does not sync those same file bytes a second time. It still rechecks every checksum, syncs newly created empty files and directory entries, and performs the existing atomic publication protocol.

The repeatable `npm run test:live` exercise builds isolated PostgreSQL state and real controller/agent processes, copies a 128 MiB file and 500 small files, verifies retrieved bytes, checks errors and progress, and kills/restarts an agent during a 256 MiB copy. It cleans generated credentials, fixtures and processes while retaining a JSON report in its temporary directory. It requires the compiled controller/agent and `MESH_TEST_DATABASE_URL` in the controller environment. `MESH_BASELINE_AGENT_BINARY` optionally compares a previous agent binary.

These timings are local loopback measurements, not a network or Windows throughput guarantee. See [the checkpoint experience report](../testing/storage-experience.md) for results and remaining friction.

## Remaining work

- Persistent endpoint discovery and gateway routing so users need no `--server` address outside the local demo.
- Windows runtime/service/ACL, long-path and power-loss testing.
- Unicode filename portability, resumable downloads, disk-space reservation and abandoned-copy cleanup.
- Richer active-operation metadata and node availability alongside last-confirmed catalogue entries.
