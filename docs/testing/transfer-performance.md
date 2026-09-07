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

## Remaining work

- Re-run on the Windows laptop when available; the earlier slow/failed network session is documented separately in [the Windows experience report](windows-storage-experience.md).
- Isolate direct HTTPS, SSH and Wi-Fi overhead before changing large-file transport. The batch change targets small-file request and journal overhead.
- Measure bandwidth-constrained paths, concurrent clients, longer outages and much larger folder trees.
- Add resumable downloads and richer operation status without weakening destination ownership or checksum validation.
- Deliver pairing, endpoint discovery and outbound routing so setup does not depend on manual SSH/firewall configuration; validate service startup against boot/logout/sleep before claiming unattended operation.
