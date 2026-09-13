# ADR 0006: Bounded file batches and foreground node operation

Status: implemented; local Windows runtime checks pass. Public-WAN and reboot/sleep service lifecycle validation remain pending.

## Transfer protocol

Agents advertise `Mesh-Transfer-Features: batch-v1`. Clients negotiate using the response to collection creation or manifest retrieval. Without this header they use the existing file/chunk endpoints. This lets new clients work with older agents; older clients continue to work with new agents.

`PUT /v1/collections/{id}/batch?indices=0,1,...` uploads whole small files. `GET` on the same route retrieves them. The body concatenates file bytes in manifest order; names, sizes and checksums come from the verified manifest. Indices must be strictly increasing, identify nonempty files of at most 256 KiB, and fit both limits: 128 files and 4 MiB per batch. The upload response contains an `offsets` array matching the requested files' full sizes.

The handler authorizes the node, collection and access before reading file bytes. There is no authorization cache. A batch already authorized may finish after revocation; subsequent requests must obtain fresh authorization. The existing four-request admission limit bounds concurrently buffered uploads. Downloads stream files, with each checksum verified by the client. Empty files are verified and created locally during retrieval. Large files and partially uploaded files retain the existing chunk protocol.

For upload acknowledgements, each file is written, synced and verified. Touched directories are synced after the batch's entries exist. SQLite then commits all batch offsets in one FULL-synchronous transaction. A failed or interrupted batch before that commit has no acknowledged offsets; any disk tails are overwritten on retry. A lost response after commit is recovered by reading durable progress, not blindly replaying the write. Final publication still verifies the collection and performs the existing publication protocol. Windows directory-sync limitations remain unchanged; this is not a power-loss certification.

Batch upload overlaps the file write/sync/verification stage with at most four workers. Jobs reference disjoint slices of the existing bounded request body rather than copying payloads. The store mutation lock remains held through the entire batch, including directory syncs and the journal commit. A worker failure cancels pending jobs and all workers finish before the staging root is closed or another mutation can begin. No offsets commit until every file worker succeeds. This improves concurrency within a batch; it does not allow independent uploads to mutate the store concurrently.

Source scanning reuses one lazily allocated 32 KiB hashing buffer per scan. It remains sequential, retains per-file progress and cancellation checks, and does not cache file hashes across copies.

Known-size upload bodies use one exact allocation; unknown-length legacy requests retain the same 4 MiB bound. Batch offset checks query only the selected file keys, and a prepared statement applies their updates within the existing atomic transaction. Begin and Finish retain full progress validation. See the [codebase audit and measurements](../testing/codebase-audit.md).

Batch retrieval opens the published collection root once per request. The client reads that single response sequentially and hands complete file buffers to at most four verification/write/sync workers through an unbuffered channel. Four worker buffers plus the producer buffer use at most 1.25 MiB at the existing file-size limit, excluding other runtime allocations. Progress callbacks remain serialized. A worker failure cancels the request and closes the body; all workers finish before destination cleanup. There are no concurrent credential renewals or extra network streams. Exact framing and checksums remain required.

## Recovery and feedback

Managed copies retry transient connection and HTTP 503 failures up to three total attempts, waiting one then two seconds. Each attempt re-reads durable offsets using the already scanned manifest. Permanent denial, malformed acknowledgements and checksum conflicts are not automatically retried. Cancellation interrupts the wait. Managed downloads use a private sibling staging directory and synced prefix journal, verify saved prefixes before reuse, and publish the destination only after full verification; rerunning the same destination resumes across connection or process failure.

Transient connection failures include interrupted JSON response bodies after headers have arrived. Such a response may follow a durable commit, so recovery still reconciles saved offsets rather than replaying an uncertain write.

Structured response headers distinguish admission pressure from unavailable controller authorization. Messages give the next action without dumping remote bodies or credentials. Scanning reports bytes hashed and completed file counts. Transfer progress includes measured average speed and an approximate remaining time, excluding bytes reused at the start of that attempt. This is phase progress, not a promise of completion time.

## One foreground node command

After enrollment, `mesh-agent run --root <dedicated-folder>` runs heartbeats and enrolled storage in one process. It accepts the same storage listen/TLS settings as `storage serve`. A single owner holds the identity lock; storage and heartbeat tasks share only immutable credential values. Logs are serialized. A permanent heartbeat failure or a storage-listener failure cancels both tasks and releases their locks. Ctrl+C stops both. `run` without `--root` keeps the existing heartbeat-only behavior.

This removes the need to manage two foreground processes. The later guided setup and Windows service compose that lifecycle without requiring SSH or router forwarding. Public certificates remain a cloud-deployment concern, and the release artifact is not yet a signed native installer.

## Next onboarding steps

The implemented guided flow pairs with a short-lived code, selects a storage folder, discovers the authenticated outbound relay, optionally provisions compute, and optionally installs same-user service hosting. The remaining onboarding evidence is signed distribution plus real boot, logout, sleep, WSL, and separate-network validation.

Measured results and reproduction commands are in [the performance report](../testing/transfer-performance.md).
