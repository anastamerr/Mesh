# Codebase and transfer-efficiency review

September 8, 2026. This pass starts from the previous working tree, including its four-worker batch uploads and reusable scan buffer. It is an incremental comparison, not another comparison against the older serial uploader.

## Scope and architecture

Reviewed the native storage, CLI, controller client, identity, heartbeat, inventory and executable packages; control-plane controllers, services, contracts, database queries and migrations; operator scripts, live-test tools, CI and existing regression coverage. Vendored dependencies were not rewritten. There is no implemented compute engine or frontend to optimize: the executor is still a skeleton.

The architecture already keeps bulk data off the controller. PostgreSQL owns enrollment, presence, scoped grants and catalogue metadata. The native storage server owns ordinary files and a SQLite journal. A copy scans a manifest, obtains a grant, reconciles offsets, transfers data and verifies publication. Reads verify the manifest and downloaded checksums. The controller is consulted for every authorized storage request.

The repository interfaces, transaction helper, state-directory owner and runner test seams serve concrete purposes. Removing these would duplicate ownership/security logic or reduce the ability to test failures. No generic transport framework, scheduler, plugin layer, extra database, dependency or protocol version was introduced.

## Changes selected

- **Allocate known upload bodies once.** The server reads exactly the validated batch size or bounded Content-Length. Unknown-length legacy uploads retain a bounded fallback. Truncated, trailing and oversized bodies fail before file mutation. The client fills one exact-size batch buffer rather than growing a buffer and copying each file through another scratch buffer. Replayable byte readers still support credential renewal.
- **Bound journal work by the batch.** The old batch writer loaded and validated all collection offsets, even for two selected files out of 10,000. The new query checks the selected primary keys and requires every selected row to exist at offset zero. Begin and Finish still validate the full progress document. Batch updates reuse a prepared statement within the same FULL-synchronous transaction. The mutation lock and durability barriers are unchanged.
- **Simplify ownership and acknowledgements.** Download workers publish the first error through the existing wait group; the producer observes context cancellation instead of repeatedly locking a second error mutex. The batch GET handler no longer wraps its body in an immediately invoked function. Four control operations share one positive-acknowledgement check in place of repeated checks and a POST wrapper that only renamed another method.
- **Resolve exact catalogue IDs correctly.** A full ID wins over matching display names and stops pagination once found. Name-only lookup still scans remaining pages to reject ambiguity. This fixes the case where display names could make the instruction to use a full ID ineffective.
- **Recover interrupted acknowledgements.** A connection lost after response headers used to surface as a raw body-read error, bypassing upload recovery. It now enters the existing bounded retry loop and reconciles durable offsets. A new real-HTTP regression fails against the pre-audit source with `unexpected EOF` and passes after the fix without retransmitting the committed batch. Fully received malformed JSON remains a permanent error; cancellation remains cancellation.

Changes preserve collection IDs, manifest encoding, chunk/batch bounds, authorization checks, offset reconciliation, exclusive download destinations, checksums and publication ordering. Tests cover new framing and catalogue boundaries plus missing/nonzero selected journal rows.

## Research and decisions

[Rclone documents bounded buffer memory and backend-dependent multithreading](https://rclone.org/docs/#multi-thread-streams); it also notes that local-to-local copies normally disable multithreading because they can be faster without it. This supports measuring each stage and bounding concurrency rather than increasing stream counts everywhere. Mesh retains its existing four file workers and four admitted requests.

[Syncthing's synchronization design](https://docs.syncthing.net/users/syncing) identifies reusable content with block hashes. [The rsync algorithm](https://download.samba.org/pub/unpacked/rsyncweb/tech_report/node2.html) sends references to matching existing blocks and transfers the unmatched bytes. These approaches are relevant to incremental copies. Applying them to Mesh would require explicit cross-collection reuse, block metadata, source-change handling and recovery contracts. They are not a cost-free optimization for a fresh immutable upload, so this pass does not add a delta engine.

[SQLite documents the durability difference between FULL and NORMAL synchronization](https://sqlite.org/pragma.html#pragma_synchronous). NORMAL can lose a committed WAL transaction after power loss. This pass retains FULL and reduces unnecessary query/buffer work instead of removing syncs.

Further network optimization needs controlled HTTPS/LAN/WAN measurements of request latency, bandwidth, disk time and controller authorization. Large uploads still acknowledge sequential 4 MiB chunks, the store still serializes mutations, and downloads still do not resume after failure. These are explicit remaining limits, not claims of state-of-the-art performance.

## Measured results

Windows amd64, Intel i7-10750H, Go 1.27.1, local disk and HTTP loopback. Five paired samples with three iterations each, alternating which version runs first. Both versions use identical benchmark fixtures. Setup is excluded; checksums, file syncs and SQLite FULL commits remain timed. These medians include every final sample; [raw samples and metadata](measurements/2026-09-08-audit-comparison.json) are retained. Earlier exploratory runs overlapped environment setup and are not used here.

| Fixture / metric | Before this audit | After | Change |
| --- | ---: | ---: | ---: |
| 32 MiB scan/upload/download: allocated bytes | 85.30 MB | 38.72 MB | 54.6% less |
| 500 × 4 KiB upload/download: allocated bytes | 73.60 MB | 66.13 MB | 10.2% less |
| Two-file batch, 10,000-entry journal: time | 13.57 ms | 4.26 ms | 68.6% less / 3.19× faster |
| Same journal operation: allocated bytes | 494.35 KB | 77.14 KB | 84.4% less |
| Same journal operation: allocation count | 30,248 | 209 | 99.3% fewer |

Allocated bytes are cumulative Go heap allocations per operation, not peak RAM or RSS. Decimal MB/KB are used above. The large fixture times both directions; it is not a one-way bandwidth benchmark. The small-folder fixture includes a simulated 20 ms authorization delay per request in both versions; the large-file fixture does not. The journal fixture includes actual file writes, syncs and offset commit but excludes collection registration and cache warming.

End-to-end speed is mixed: large-file median **561.3 → 573.0 ms** (2.1% slower), small-folder **1079.7 → 1052.9 ms** (2.5% faster). The 128-entry journal fixture measured **3.34 → 3.71 ms** (10.9% slower, 0.37 ms absolute). These samples do not establish a universal speed improvement or absence of a performance regression at small sizes. They support the allocation reduction and large-journal scaling gain. Small-folder request count remains 11 per round trip. Storage latency varies substantially, including a 3.07-second baseline small-folder sample; no final outliers were removed.

Reproduce with `go test ./internal/storage -run '^$' -bench 'BenchmarkLargeFileTransfer|BenchmarkBatchJournalScaling|BenchmarkSmallFolderTransfer/legacy=false' -benchtime=3x -count=5 -benchmem` from `agent`. For a paired comparison, compile each source version with `go test -c` and alternate executions as described above. The baseline is the preserved pre-audit working tree, not Git HEAD, which predates the previous parallel-upload changes.

## Regression validation

- Go race tests across every package, `go vet`, formatting of changed Go files and the Windows executable build pass. Unchanged files still appear in a repository-wide Go 1.27 gofmt listing; they were not mechanically rewritten. Linux amd64 and macOS arm64 cross-builds are compile checks, not native runtime tests.
- Anti-slop lint, strict TypeScript typecheck and production build pass without changing lint rules or dependency locks.
- All 13 backend tests pass with no skips against disposable PostgreSQL 17.11 and the compiled Windows agent. Database migrations pass twice, including their idempotent rerun. Coverage includes controller restarts, revocation, concurrency and agent storage integration.
- The [live Windows report](measurements/2026-09-08-audit-live.json) records verified 128 MiB and 500-file copies, retrieval, repeat copies, and a killed-server upload resumed and retrieved with matching hashes for 256 MiB. These are loopback/local-disk checks, not remote-network throughput measurements. RSS sampling is unavailable on this run; its zero field is not a memory measurement.
- After the live run, the upload handlers retained `http.MaxBytesReader` around the exact-size reader to preserve oversized-body connection handling. The full Go race suite, vet and host build passed again; final comparative benchmarks include this wrapper.

The interrupted-acknowledgement regression was also run against the preserved pre-audit source: it fails with `unexpected EOF`. The fixed source passes and sends the committed batch only once. This is evidence for that recovery behavior, not a guarantee against every possible regression.

## Remote backend prune pass — 2026-09-08

Reviewed pairing, identity persistence, relay transport, managed CLI integration and resumable retrieval added in `f605ae7`. The code/test diff removes 44 net lines across 11 files. Removed an unused node-list client and response fields, a redundant pairing-ID argument, duplicated mock approval state, and the certificate's PEM encode/decode round trip. Downloads reuse one file/hash writer per file rather than per checkpoint and share full-file recovery verification. Relay bridges reuse two connection wrappers instead of four. Existing protocol, journal and database formats remain unchanged.

Relay-ticket creation now authorizes, expires old tickets and inserts the new ticket in one SQL statement, reducing successful issuance from three database round trips to one. Device and consumer credentials remain distinct, and tickets still expire at the earliest source expiry or ten minutes. This operation-count reduction is not a claim of threefold end-to-end throughput.

Validation: unchanged anti-slop rules, strict typecheck/build, all 13 backend tests with PostgreSQL and compiled agent (no skips), full Go race tests, vet, formatting, host builds, and Linux/macOS/Windows amd64 cross-builds pass. The [final live report](measurements/remote-prune-2026-09-08.json) records actual controller/agent/HTTPS-relay processes: 128 MiB upload, killed retrieval and relay restart, 12 MiB reused, corrupt-prefix repair, wrong-key rejection, destination protection, 200 small-file hash checks and revocation. Local upload/recovery times are observations, not controlled before/after speed claims. Separate-network WAN validation remains outstanding.
