# Initial HTTP contract

All routes return JSON. Errors use Nest's HTTP error shape. Secret-bearing responses use `Cache-Control: no-store`. Versioned business routes use `/v1`; health routes are unversioned.

| Method | Route | Authentication | Success |
| --- | --- | --- | --- |
| GET | `/health/live` | None | 200 process alive |
| GET | `/health/ready` | None | 200 schema reachable; otherwise 503 |
| POST | `/v1/enrollment-tokens` | Operator bearer | 201 token and expiration; shown once |
| POST | `/v1/nodes/enroll` | Enrollment token in body | 201 node, node credential, credential expiration |
| GET | `/v1/nodes` | Operator bearer | 200 latest 100 nodes, without credentials |
| POST | `/v1/nodes/:id/heartbeat` | That node's bearer | 200 accepted |
| POST | `/v1/nodes/:id/revoke` | Operator bearer | 200 revoked, including repeat revocation |

Enrollment body:

```json
{
  "enrollmentToken": "<one-time token>",
  "name": "Lenovo",
  "platform": "windows",
  "architecture": "amd64",
  "agentVersion": "0.1.0-dev"
}
```

Heartbeat body:

```json
{
  "sequence": 0,
  "inventory": {
    "cpuLogicalCores": 8,
    "memoryTotalBytes": 17179869184,
    "memoryAvailableBytes": 8589934592
  }
}
```

Send every 15 seconds as an initial client policy. Increase and persist the sequence for every new observation. Nonincreasing sequences return 409 without changing presence; bad/expired/revoked credentials return 401. Unknown request fields and invalid inventory return 400. Revocation of a missing UUID returns 404.

`unknown`: no heartbeat yet. `online`: last accepted receipt within 60 seconds. `unreachable`: older receipt. `revoked`: operator disabled identity. Inventory is self-reported and retains its last-seen timestamp; it is not an attestation. Disk/GPU inventory and execution environments are deferred to explicit contracts.

## Native storage data API

The separate agent endpoint is documented in [ADR 0003](../docs/decisions/0003-native-storage.md), including manifest, chunk, authorization, recovery and publication semantics. It does not run on the control-plane port.

Controller-issued transfer grants and enrolled agent validation are defined in [ADR 0004](../docs/decisions/0004-storage-authorization.md).

## Collection catalogue

- Operator `POST /v1/nodes/{id}/collections`: `{id, name, fileCount, totalBytes}` registers/renames a requested copy; returns `{accepted: true}`.
- Operator `GET /v1/nodes/{id}/collections?after={collectionId}`: up to 100 catalogue entries (`id`, `name`, `fileCount`, `totalBytes`, `confirmedAt`), sorted by ID. A null confirmation means pending.
- Node-authenticated `POST /v1/nodes/{id}/collections/confirm`: `{id, fileCount, totalBytes}` confirms that node's local publication; returns `{accepted: true}`.

Details and consistency limits: [ADR 0005](../docs/decisions/0005-collection-workflow.md).

## Native storage batching

Agents advertise `Mesh-Transfer-Features: batch-v1`. Negotiated clients use `GET`/`PUT /v1/collections/{id}/batch?indices=...` for strictly ordered small-file indices, bounded to 128 files, 256 KiB per file and 4 MiB total. Upload acknowledgements contain all committed `offsets`. Each request is scoped and authorized, and file checksums remain mandatory. Clients fall back to individual file endpoints when the feature is absent. See [ADR 0006](../docs/decisions/0006-transfer-efficiency.md) for durability and compatibility semantics.
# Pairing and remote transport

See [paired remote storage](../docs/remote-access.md) for deployment, CLI flow, authorization boundaries and recovery guarantees. Pairing uses `POST /v1/pairing-challenges`, operator listing/approval, and proof-authenticated polling. `GET /v1/network` publishes the relay origin; operator `GET /v1/nodes/:id/connection` returns the paired key fingerprint. Active paired devices renew through `POST /v1/nodes/:id/renew`. `POST /v1/nodes/:id/relay-tickets` exchanges a device credential or storage grant for a role-bound routing ticket; only the separate relay service credential can call `POST /v1/relay/authorize`. Storage authorization remains inside the end-to-end TLS tunnel.

## Compute workloads

The operator creates and manages explicitly placed workloads; an assigned node retrieves desired state and reports observations. Full invariants and runtime restrictions are in [ADR 0007](../docs/decisions/0007-compute-reconciliation.md).

| Method | Route | Authentication | Purpose |
| --- | --- | --- | --- |
| POST | `/v1/workloads` | Operator bearer | Idempotently create a job or application |
| GET | `/v1/workloads` | Operator bearer | List the latest 100 workload records |
| POST | `/v1/workloads/{id}/state` | Operator bearer | Change an application's desired state |
| POST | `/v1/nodes/{id}/execution-environments` | That node's bearer | Report stable executor readiness |
| GET | `/v1/nodes/{id}/execution-environments` | Operator bearer | Inspect executor readiness and last contact |
| GET | `/v1/nodes/{id}/workloads` | That node's bearer | Retrieve that node's assignments |
| POST | `/v1/nodes/{id}/workloads/{workloadId}/observations` | That node's bearer | Report current-revision observed state |
| POST | `/v1/nodes/{id}/workloads/{workloadId}/relay-ticket` | That node's bearer | Issue a device ticket for an observed-running application |
| POST | `/v1/workloads/{id}/connection-ticket` | Operator bearer | Issue an operator connection ticket for an observed-running application |

Images are digest pinned. `command` is a 1–64 element argument array rather than a shell string. CPU is expressed in millicores and memory in bytes. `inputCollectionId` is null or a confirmed collection on the same node. Jobs require `desiredState: "running"` and `servicePort: null`; applications accept running/stopped and require one port.

Workload creation is rejected until the assigned node reports a ready `docker-linux` execution environment. The environment has its own stable UUID and last-seen time, so it is not counted as another physical node and Docker IDs never become Mesh identities.

```json
{
  "id": "a caller-generated UUIDv4",
  "nodeId": "the explicitly selected node UUIDv4",
  "name": "Transcode video",
  "kind": "job",
  "image": "registry.example/transcoder@sha256:<64 lowercase hex characters>",
  "command": ["convert", "/mesh/input/video.mp4", "/mesh/data/output.mp4"],
  "resources": { "cpuMillis": 2000, "memoryBytes": 536870912 },
  "inputCollectionId": "<confirmed collection SHA-256 or null>",
  "servicePort": null,
  "desiredState": "running"
}
```

Observations contain `revision`, `state`, nullable `exitCode`, nullable `failureCode`, and nullable `outputCollectionId`. A successful job is reported only after `/mesh/data` has been exported, checksum-verified, and confirmed as a collection on the assigned node; its output collection ID is then required. `exporting` is a retryable state between container exit and publication. Terminal success/failure requires an exit code, and only failure has one of the bounded failure codes. Stale or regressing observations return 409 without changing the stored observation.

Application relay routes are `app-{workload UUID}` and never share a waiting queue or authorization subject with storage or another application. Tickets are valid for at most ten minutes and authorize successfully only while the referenced application's current revision remains desired and observed running on its assigned active node. The relay carries opaque bytes; the operator verifies the paired device public key inside an end-to-end TLS stream before application traffic is sent. See [ADR 0009](../docs/decisions/0009-authenticated-application-routing.md).
