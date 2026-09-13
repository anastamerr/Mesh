# MVP and local connectivity merge verification

Date: 2026-09-13. Incoming main: `1ab87f55483961695a85799bf4cfb599ff45237c`. Local changes originated on `212a6ca`.

The merged working tree retains workload reconciliation, verified job-output collections, WSL provisioning, authenticated application routes, guided setup, and Windows service hosting. It also retains private-LAN storage candidate publication, authenticated direct selection with relay fallback, controller/relay limits and metrics, monitored deployment, and physical-device validation runners.

The shared runner owns one storage store for transfers and job-output publication. LAN storage and application relay serving use the same paired device identity. `--direct-lan` is carried through guided setup and service installation, including the PowerShell wrapper's `-DirectLAN` switch. This discovers addresses of already paired nodes through the controller; it does not discover or enroll arbitrary nearby laptops. Applications continue to use workload-scoped relay routes.

Duplicate secret-file parsing was replaced with one bounded, symlink-rejecting loader, and device TLS verification reuses the shared pairing verifier. Both deployment layouts now share controller/relay Dockerfiles while retaining their distinct certificate and monitoring arrangements. The Caddy deployment blocks public metrics. Connectivity's ADR is now 0013 to avoid colliding with compute's ADR 0007. Migration filenames and checksums are unchanged: the migration ledger keys on full filenames, so both `006_direct_candidates.sql` and `006_workloads.sql` remain compatible with previously applied histories.

## Validation

| Check | Result |
| --- | --- |
| Lint, TypeScript typecheck and controller build | Pass |
| Backend tests with PostgreSQL and compiled agent | 26 passed, none skipped |
| Migration upgrades from local connectivity, incoming MVP, and empty schema | Pass; second runs apply nothing and preserve sentinel data |
| Complete Go race suite | Pass |
| Affected CLI/storage race tests after final integration edits | Pass |
| Go vet, formatting, host agent/relay builds, Windows amd64 cross-build | Pass |
| Real-process HTTPS relay and automatic LAN selection | Pass |
| Real-process native storage workflow and interrupted-upload recovery | Pass |
| Both Compose YAML files and CI YAML syntax; cloud initializer shell syntax | Pass |
| Docker image builds and running deployment stacks | Not run: Docker unavailable on this host |
| Physical Windows/WSL/service lifecycle and public WAN | Not run in this merge pass |

Go/runtime checks used private temporary directories with `umask 077`. Socket and database checks ran with local networking enabled, outside the restrictive command sandbox. Test database changes were confined to isolated schemas and cleaned up.

The real-process checks covered a 128 MiB relayed transfer, downloader termination and durable resume after relay restart, wrong-device-key rejection, corrupted-prefix recovery, destination preservation, 200 relayed and direct small-file hash comparisons, and revocation. The separate storage workflow covered 128 MiB and 500 small files, repeat-copy behavior, catalogue state, errors, interrupted upload, and hash verification. These are local measurements, not WAN throughput or Windows readiness claims. [Recorded measurements](measurements/2026-09-13-mvp-connectivity-merge.json).

The original local files remain backed up in the `mesh-local-pre-mvp-merge-20260913` stash and `/private/tmp/mesh-local-before-merge-20260913.tar.gz`. Main has been fast-forwarded to the incoming merge; the reconciled local changes remain uncommitted for review.
