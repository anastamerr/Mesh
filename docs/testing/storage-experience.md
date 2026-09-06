# Collection workflow: live experience check

Date: 2026-09-06. macOS/Apple Silicon, local disk and loopback HTTP, real compiled Go processes, real controller and isolated PostgreSQL schema. No mocks in the live exercise.

## What was actually used

The test invoked the same `mesh-local.mjs` entry point as `npm run mesh`, with node names and folder names. It copied and retrieved a 128 MiB file and 500 × 4 KiB files, preserving empty directories and checking SHA-256 for every file. It checked a missing selection, refused an existing destination, repeated existing copies, and killed the serving agent during a 256 MiB upload. The catalogue stayed pending until restart/resumption and agent confirmation. Retrieved bytes matched after recovery.

Generated credentials, fixtures, server processes and the isolated database schema were cleaned up. JSON evidence was retained in the temporary report directories for this session.

## Observed timings

| Operation | First managed implementation | After targeted optimization |
| --- | ---: | ---: |
| Copy 128 MiB | 578 ms | 570 ms |
| Retrieve 128 MiB | 147 ms | 147 ms |
| Copy 500 small files (1.95 MiB total) | 5,855 ms | 5,215 ms |
| Retrieve 500 small files | 3,120 ms | 2,603 ms |
| Repeat unchanged large copy | 104 ms | 101 ms |
| Repeat unchanged small-file copy | 83 ms | 67 ms |

The prior manual identify/grant/upload workflow measured 591 ms for the large fixture and 5,517 ms for the small-file fixture in the second run. It required separate commands and a token file; the managed workflow needs one copy command and scans once.

First visible feedback appeared in 43–64 ms in the second run. Sampled peak serving-agent RSS was approximately 44 MiB. This is sampled process memory, not a proven maximum or a measure of all controller/client processes.

These are individual local observations, not statistical benchmarks. Cache state, startup costs and other local activity can affect timings; they do not establish LAN/WAN, Windows, or old-laptop throughput. The change between runs was a bounded cache of the last immutable manifest and removal of a redundant final file sync. Authorization, publication-state queries, checksums and required durability syncs remain active.

## Convenience assessment

- The new command eliminates manual IDs and persistent grant files for ordinary copying.
- Catalogue names make retrieval practical. Full IDs remain visible for disambiguation.
- The first message is immediate, and longer transfers show periodic progress.
- “Verifying” is distinct from uploaded-byte completion.
- A resumed copy explicitly reports bytes already present.
- Missing names and occupied destinations explain the next action.
- Interruption tells the user to rerun the same copy command; the test did so successfully.

Example recovery transcript:

```text
Connecting to controller...
Scanning folder and calculating checksums...
Uploading: 4.0 MiB / 256.0 MiB (2%); 4.0 MiB already present
Verifying: 256.0 MiB / 256.0 MiB (100%); 4.0 MiB already present
Complete: 256.0 MiB / 256.0 MiB (100%); 4.0 MiB already present
```

## Remaining friction

Small files are still disproportionately expensive. Each file involves authorization, filesystem work and durability operations. This checkpoint improves the measured path but does not claim it is fully optimized. A later measured change could investigate bounded parallel retrieval or a batched-file protocol; neither should weaken revocation or durable acknowledgements.

Remote users still need an endpoint address. Unicode filenames and resumable downloads remain unsupported. Scanning emits a phase message but no per-file scan progress. Catalogue confirmation is historical publication evidence, not a live availability or backup guarantee.

## Repeat

Build the controller and native agent, configure `MESH_TEST_DATABASE_URL` in the controller `.env`, then run:

```sh
npm run test:live
```

Optionally set `MESH_BASELINE_AGENT_BINARY` to a previous compiled agent. The runner prints the path to its retained JSON report. It does not use or modify existing node identities or collections.
