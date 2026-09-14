# Mesh MVP backend readiness review

Date: 2026-09-14. Review, hardening, and physical-device validation on a Windows 11 Lenovo IdeaPad Gaming 3.

## Current assessment

The backend supports a single-owner technical MVP of pairing, native file collections, authenticated outbound relay access, and typed container workloads. Physical Windows validation now covers WSL2 provisioning, Docker readiness, a digest-pinned job, verified output publication, and authenticated retrieval. This is not evidence that the complete consumer experience or public deployment is ready to promote. Windows unattended reboot/sleep behavior and genuinely separate-network internet access remain release gates.

Mesh executes applications on the contributed machine using that machine's CPU and RAM. It does not transparently add remote memory to applications on a different computer. An internet connection on both devices also requires an available public controller and relay; the hosting machine must remain awake and connected.

## Corrections in this pass

- Fixed concurrent workload admission. With 99 occupied slots, the original implementation accepted four simultaneous creates and reached 103. The regression forces all four contenders to wait on the node lock. The corrected implementation admits one and rejects three.
- Captured immutable node identity fields before heartbeat goroutines start replacing the persisted state struct, removing a shared-state race in compute startup.
- Added two-worker reconciliation with cancellation and bounded readiness/executor invocation times. Failed executor process launches no longer become fabricated terminal job failures, and independent assignments still proceed.
- Set explicit per-container swap and rotating log limits, including on native Linux Docker installations. Quiet image pulls avoid treating large progress output as a failed pull.
- Close unconsumed connection results when cancellation wins the direct/relay connection race.
- Reject case-aliased Windows identity/storage directories and retain custom relay CA arguments in the suggested post-setup startup command.
- Stop restarting and paused application containers before acknowledging stopped state or replacing an older revision. A runtime stop failure is not acknowledged as success.
- Use the short `wslpath -a -u` options supported by the packaged Ubuntu 24.04 environment. The former long options caused every Windows job with a native input or output path to remain assigned and retry forever.
- Make strict-directory tests explicitly create private state/storage roots so Go 1.27's test temporary-directory mode does not depend on the runner's process umask.
- Generate Windows artifact checksum sidecars from inside the package directory and verify them before upload, leaving portable basenames that work with ordinary `sha256sum --check` after extraction.

The existing storage mutation lock, SQLite FULL durability, four-request transfer admission, checksums, scoped authorization, and pinned device-key verification remain in place. There is no measured claim of higher overall throughput from the compute worker change.

## Verification

| Check | Evidence / status |
| --- | --- |
| Controller strict typecheck, anti-slop lint, build | Passed |
| Controller, PostgreSQL, migration upgrades, compiled Windows agent | 26 tests passed, zero skipped |
| Admission regression against original source | Failed as expected: four accepted instead of one; fixed source restored |
| Native Go tests, vet, host build | Passed; affected packages rerun after subsequent fixes |
| Linux amd64 and macOS arm64 cross-builds | Passed; compilation only, not native runtime validation |
| Windows race detector | Full suite passed with Go 1.27.1 and GCC 16.2; affected connectivity/executor/compute packages passed again after the final code changes |
| Actual HTTPS relay and native Windows agent processes | Passed: 128 MiB transfer, durable resume after downloader termination and relay restart, corrupt-prefix recovery, wrong-key rejection, existing-destination preservation, 200 relayed/direct file hashes, and revocation |
| Actual local storage/operator workflow | Passed: large-file and small-file copies, retrieval, repeat-copy and interruption checks |
| Windows WSL2/Docker job and output retrieval | Passed on the IdeaPad: verified bundle import, Docker 29.1.3 amd64 readiness, digest-pinned Alpine job, `succeeded` observation, immutable output collection, authenticated 15-byte retrieval, and exact `mesh-compute-ok` content |
| Service reboot/logout/sleep and separate-network WAN | Still deferred; service installation needs the target account credential and no public controller/relay endpoint was supplied |

The database tests use disposable schemas in a temporary PostgreSQL 17.11 instance bound to loopback. They do not alter a configured application database. The temporary database server was stopped after verification. Native tools and test credentials are temporary; no credentials or generated executables are tracked.

Raw reports: [relay/direct workflow](measurements/2026-09-13-mvp-review-remote.json) and [storage workflow](measurements/2026-09-13-mvp-review-storage.json). Timing values are local observations under other development activity, not controlled comparative benchmarks. The storage report's zero RSS field means measurement was unavailable, not zero memory use. These process reports precede the final connection-cancellation cleanup; its affected package is verified separately.

## Test handoff and product limits

1. On the prepared Windows device, run guided setup with a public HTTPS controller and relay, then verify identity reload, storage upload/retrieval, and unattended startup under the same account.
2. On a client using a genuinely different internet connection, upload and retrieve a representative folder. Verify hashes, interrupt both directions, restart the relay, repeat the operation, and confirm revocation prevents renewed access.
3. Repeat the now-passing digest-pinned job with a representative confirmed input collection, then load-test exported output. The no-input job, output publication, and authenticated retrieval have passed on the target hardware.
4. Test logout, reboot before login, sleep/resume, network loss, controller outage, and Docker unavailability. Confirm native storage remains usable when compute is unavailable and that failed setup can be resumed safely.
5. Measure on the old hardware: time to first feedback, throughput in both directions, CPU, peak RAM, free disk, and recovery time. Avoid drawing WAN or low-memory performance conclusions from this host's loopback results.

Known limitations must remain visible in the MVP positioning:

- File collections currently require Windows-compatible ASCII names, contain at most 10,000 entries, and reject symlinks/special files. Ordinary file contents are unrestricted; “upload anything” is broader than the current filename and collection contract.
- The current application model is a digest-pinned Linux container with typed arguments, not arbitrary Windows applications, desktop streaming, or combined distributed RAM.
- Resource limits apply per container. Aggregate host resource reservation, hard disk quotas, storage deletion/retention, and automatic cleanup of exported job scratch data remain absent.
- Compute failure codes, runtime availability, stale observations, and a disconnected device must be distinguished in the future UI. Last reported running is not proof of current availability.
- Single-owner operator credentials, CLI workflows, and a separately hosted public gateway remain prerequisites. Multi-user accounts, a browser upload experience, backup/replication, and automatic failover are outside this backend MVP.

## Technical references

PostgreSQL documents statement snapshots and row-lock behavior in [transaction isolation](https://www.postgresql.org/docs/18/transaction-iso.html). Docker documents [memory/swap limits](https://docs.docker.com/engine/containers/resource_constraints/) and [local log rotation](https://docs.docker.com/engine/logging/drivers/local/). These explain the mechanisms used above; regression and runtime results provide the evidence for Mesh itself.
