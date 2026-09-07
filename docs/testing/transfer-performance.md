# Transfer efficiency checkpoint

Date: 2026-09-07. Measurements below are local macOS/Apple Silicon observations. They do not establish Windows, Wi-Fi, WAN, or state-of-the-art performance.

## Controlled latency

`BenchmarkSmallFolderTransfer` copies and retrieves 500 × 4 KiB files using real filesystem writes, SQLite FULL-synchronous commits and HTTP. Its authorizer adds 20 ms per request. The legacy comparison suppresses feature negotiation so the same implementation uses individual file/chunk endpoints.

| Path | Copy + retrieve | Authorized requests |
| --- | ---: | ---: |
| Individual file requests | 29.30 s | 1,003 |
| Batches with grouped durable commits | 4.56 s | 11 |

That is approximately 6.4× faster in this controlled scenario, with 98.9% fewer requests. An intermediate version that batched HTTP but committed each file's journal separately took 6.76 s. No file-content sync, checksum check, per-request authorization, or final publication check was removed.

These are single measured iterations, not statistical throughput guarantees. The benchmark models per-request latency; it does not emulate bandwidth loss, packet loss or all behavior of the SSH setup.

Run from `agent`:

```sh
go test ./internal/storage -run '^$' -bench BenchmarkSmallFolderTransfer -benchtime=1x -count=1
```

## Real controller and agent processes

The live runner uses an isolated PostgreSQL schema and compiled agent processes. The current agent runs heartbeats and storage with `run --root`; the previous binary uses its existing separate storage command.

| Operation | Previous binary | Optimized managed workflow |
| --- | ---: | ---: |
| Copy 128 MiB | 567 ms | 516 ms |
| Copy 500 × 4 KiB files | 5,106 ms | 2,253 ms |
| Retrieve 128 MiB | Not measured in this comparison | 134 ms |
| Retrieve 500 small files | Not measured in this comparison | 2,112 ms |
| Repeat unchanged 128 MiB copy | Not measured | 98 ms |
| Repeat unchanged small-file copy | Not measured | 65 ms |

The previous-copy measurement includes its identify/grant/upload workflow; the controlled benchmark above is the cleaner protocol comparison. First feedback appeared within 43–63 ms. Sampled serving-agent peak RSS was about 39 MiB; sampling is not proof of a hard memory maximum.

Every fixture was checked against both native stored files and retrieved files. The runner also checked catalogue confirmation, empty directories, missing names, existing destinations, a real process kill during a 256 MiB upload, visible retry exhaustion, restart, resumed bytes and final retrieval integrity. The report returned PASS. Evidence for this run was retained at the session's temporary `mesh-experience-6bNkiF/report.json` location.

A final run after pruning an unused chunk-buffer allocation and noisy short-scan feedback also passed (`mesh-experience-kGvhp7/report.json`): 128 MiB copy/retrieval took 577/144 ms and small-file copy/retrieval took 2,256/2,129 ms.

Additional tests cover batch byte/file limits, malformed indices, wrong access scope, revocation, checksum failure, atomic rollback on journal error, truncated download cleanup, lost batch acknowledgements without retransmission, retry cancellation and permanent denial. Combined-node lifecycle tests verify authenticated storage, heartbeats, revocation shutdown and released locks.

Repeat the real-process check from the repository root:

```sh
npm run test:live
```

Set `MESH_BASELINE_AGENT_BINARY` to a previous agent to include the copy comparison. Build the controller and agent first; configure `MESH_TEST_DATABASE_URL` in the ignored controller environment.

## September 8: bounded download workers

`BenchmarkReceiveBatch` compares sequential reception with four local workers on the same 64 × 4 KiB fixture, retaining hashing, exclusive file creation and file syncs. Three single-iteration samples on the development Mac took 270.20/270.68/276.73 ms sequentially and 188.99/194.80/208.49 ms with workers. Median time fell from 270.68 to 194.80 ms (28%). This isolates local receive/write/sync work; it is not a Windows or end-to-end throughput claim.

```sh
go test ./internal/storage -run '^$' -bench '^BenchmarkReceiveBatch$' -benchtime=1x -count=3
```

The real-process local check passed again (`mesh-experience-imMU4g/report.json`): 128 MiB copy/get took 523/146 ms; 500 small files took 2,171/1,757 ms. Native and retrieved hashes matched, and the forced 256 MiB upload interruption/restart/resume check passed. The earlier small-file retrieval observation was 2,129 ms; these separate live runs are experience observations, not a controlled attribution of that difference to workers alone.

Batch GET also reuses one published collection root instead of reopening it and querying completeness for each file. Scan progress now reports every completed file, including empty files and the final file count.

## September 8: Windows upload parallelism and scan allocation

Measured on Windows/amd64, Intel Core i7-10750H (12 logical CPUs), Go 1.27.1. The unchanged source baseline is `fbecdd2f69108df18f3f9d4d7a6f4f89cf203e7d`. These are local filesystem/loopback measurements, not Wi-Fi, WAN, or compute-executor results.

The upload file stage now uses four workers on disjoint slices of the existing batch body. File syncs, checksums, the store mutation lock, directory syncs on supported platforms, and the FULL-synchronous atomic journal commit remain in place. Scanning reuses a single 32 KiB buffer instead of allocating one per file. No hash cache, authorization cache, or additional network streams were added.

| Measurement (median of five samples) | Before | After | Change |
| --- | ---: | ---: | ---: |
| Copy + retrieve 500 × 4 KiB files | 2.299 s | 1.768 s | 23.1% less time |
| Allocated bytes during that transfer | 90.15 MB | 73.86 MB | 18.1% fewer bytes allocated |
| Authorized requests per transfer | 11 | 11 | Unchanged |
| Local write/verify/sync, 64 × 4 KiB files, one vs four workers | 179.12 ms | 73.51 ms | 2.44× throughput |
| Allocated bytes to scan 500 × 4 KiB files | 17.50 MB | 1.14 MB | 93.5% fewer bytes allocated |

MB above means decimal millions of bytes. Allocated bytes are cumulative Go heap allocations per operation, not peak resident memory. Scan wall time was 457 ms versus 433 ms; the clear scan result is allocation reduction, not a large disk-speed improvement. The local write benchmark compares one and four workers through the same scheduler, and excludes HTTP and SQLite commits; its speedup must not be presented as an end-to-end speedup.

For the end-to-end comparison, both versions were compiled before timing. Five pairs alternated execution order (baseline first on odd pairs, optimized first on even pairs), with three iterations per sample. Both used the existing `BenchmarkSmallFolderTransfer/legacy=false`, including real file I/O, SQLite commits, download checksum verification, and an explicitly simulated 20 ms authorization delay per request. Every optimized sample was faster than its paired baseline. Baseline samples ranged from 2.287–2.441 s; optimized samples ranged from 1.669–1.987 s. Raw samples are retained in [the transfer measurements](measurements/2026-09-08-windows-transfer.json).

Earlier exploratory timings overlapped toolchain setup or other tests and varied widely; they are excluded from this comparison. No other Mesh test or compiler setup ran during the alternating measurements. Five local pairs are evidence for this fixture, not a statistical guarantee across machines or disk types. Windows still lacks the Unix directory-fsync guarantee described above.

The scan benchmark uses the identical new fixture on both source versions and checks manifest file sizes and hashes outside the timed region. The local-stage measurements use `-benchtime=3x -count=5 -benchmem`. Raw [baseline scan](measurements/2026-09-08-windows-scan-baseline.txt) and [optimized scan/one-versus-four-worker output](measurements/2026-09-08-windows-local-stages.txt) are retained.

Reproduce the local-stage measurements from `agent`:

```sh
go test ./internal/storage -run '^$' -bench '^BenchmarkScan$|^BenchmarkWriteBatchFiles$' -benchtime=3x -count=5 -benchmem
go test ./internal/storage -run '^$' -bench 'BenchmarkSmallFolderTransfer/legacy=false' -benchtime=3x -count=5 -benchmem
```

For a before/after comparison, build a storage test binary with `go test -c` for each version, then alternate those binaries with `-test.run=^$ -test.bench=BenchmarkSmallFolderTransfer/legacy=false -test.benchtime=3x -test.count=1 -test.benchmem`. Copy the new scan benchmark file into a separate baseline checkout to compare scan allocation using the same fixture. Quote each `-test.*` argument in PowerShell.

Validation on Windows passed: `go test -race ./... -count=1 -timeout=180s`, `go vet ./...`, formatting checks, and native builds of `mesh-agent` and `mesh-executor`. Race testing used LLVM-MinGW Clang 23.1.0. Added tests exercise worker bounds, first-error cancellation, worker completion before return, and concurrent files with shared nested parents. Existing checksum-failure, atomic journal rollback, authorization/revocation, and process-kill/recovery tests also passed. The controller/PostgreSQL live runner, Wi-Fi/HTTPS transfer experience, and Linux/macOS runtime suites were not rerun for this change.

## Remaining work

- Re-run the real controller/network experience on the Windows laptop; the local Windows benchmarks above do not cover that path. The earlier slow/failed network session is documented separately in [the Windows experience report](windows-storage-experience.md).
- Isolate direct HTTPS, SSH and Wi-Fi overhead before changing large-file transport. The batch change targets small-file request and journal overhead.
- Measure bandwidth-constrained paths, concurrent clients, longer outages and much larger folder trees.
- Add resumable downloads and richer operation status without weakening destination ownership or checksum validation.
- Deliver pairing, endpoint discovery and outbound routing so setup does not depend on manual SSH/firewall configuration; validate service startup against boot/logout/sleep before claiming unattended operation.

The subsequent [codebase audit](codebase-audit.md) measures incremental buffer and journal improvements against this already-parallel working tree and records the latest regression checks.
