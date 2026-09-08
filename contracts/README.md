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
