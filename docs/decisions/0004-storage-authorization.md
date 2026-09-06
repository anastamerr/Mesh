# ADR 0004: Controller-authorized storage transfers

Status: implemented; direct agent connections, no remote gateway yet.

## Scope

An operator issues a ten-minute transfer grant through the controller. Each grant permits one of:

- `write`: create/resume and finish one exact collection on one node.
- `read`: retrieve the manifest and files of one exact collection on one node.
- `list`: list published collection IDs on one node; it grants no file access.

The controller generates a random 256-bit bearer token, returns it once, and stores only its SHA-256 hash in PostgreSQL. The grant is independent of the node credential and the operator key. Issuance requires operator authorization and an enrolled, unrevoked node with an unexpired credential. Issuance records an audit event; expired grant rows are removed during subsequent issuance.

The storage agent authenticates to the controller using its node credential for every transfer request. The controller checks the grant hash, node identity, collection, access, grant expiry, node credential expiry and revocation together. No authorization decisions are cached. A grant copied to another node does not work.

Node revocation and grant expiry reject **subsequent requests**. A request already authorized may finish, including an in-flight file download or finalization. This is not continuous cancellation of an active stream. Grant expiry during an upload requires a new grant for the same collection; retrying upload resumes its durable progress. Copies remain on disk after revocation.

If the controller cannot validate a request, the agent returns 503 without performing the operation. An explicit denial returns 401. This intentionally requires controller availability and adds one controller request per storage request. We are not claiming offline authorization or tuning this path with a stale authorization cache.

## Agent modes

`mesh-agent storage serve --enrolled --root <directory> [--state-dir <identity-directory>]` uses the enrolled node's controller. It briefly locks and reads identity at startup, then releases that lock. Start it before `mesh-agent run`, or stop an existing heartbeat runner while starting it. Heartbeats can then run alongside storage. Storage does not itself publish presence. A changed identity or node credential requires restarting storage.

The legacy `--key-file` serving mode is limited to literal loopback listeners, even with TLS. It remains a development convenience and does not consult controller revocation. The CLI rejects mixing enrolled serving and a local shared key. Non-loopback serving requires enrolled mode plus a TLS certificate and key. Clients validate certificates normally.

The same private `--key-file` option on upload/list/download accepts a scoped grant token. Clients never need the operator key or the server's node credential. Windows directory/key ACL provisioning, service hosting and runtime validation remain pending.

## Controller HTTP contract

| Method | Path | Authentication | Body | Response |
| --- | --- | --- | --- | --- |
| POST | `/v1/nodes/{id}/storage-grants` | Operator bearer key | `{access, collectionId}` | 201 `{token, expiresAt, nodeId, permission}` |
| POST | `/v1/nodes/{id}/storage-grants/validate` | That node's bearer credential | `{token, permission: {access, collectionId}}` | 200 `{accepted: true}` or denial |

`collectionId` must be a lowercase 64-character SHA-256 ID for read/write and explicitly `null` for list. Unknown properties and access types are rejected. UUIDs must be version 4. Node credentials cannot mint grants; the operator key cannot impersonate the node when validating them.

The storage data API remains as defined in ADR 0003. It derives authorization scope from the actual operation and, for create/resume, the submitted manifest's canonical ID. A caller cannot substitute a separate authorization scope for different uploaded content.

## Local workflow

First apply migrations (`npm run db:migrate`), start the controller (`npm run dev`) and enroll an agent (`npm run agent:enroll`). Build the native agent as described in its README.

From the project root, start storage in another terminal:

```sh
agent/bin/mesh-agent storage serve --enrolled --root "$HOME/.mesh-local/enrolled-files"
```

Determine the node ID with `agent/bin/mesh-agent status`. Calculate the collection ID before requesting write permission:

```sh
agent/bin/mesh-agent storage identify --source /absolute/path/to/folder
npm run storage:grant -- --node NODE_UUID --access write --collection COLLECTION_ID --out /private/path/write-grant.key
agent/bin/mesh-agent storage upload --server http://127.0.0.1:7332 --key-file /private/path/write-grant.key --source /absolute/path/to/folder
```

The development helper reads the local controller `.env`, calls only its loopback API, and writes the token to a new 0600 file. It prints expiry, never the token. It refuses to overwrite an existing file. Use a new filename when replacing an expired grant. A folder changed after `identify` needs a new collection ID and write grant.

For retrieval and listing:

```sh
npm run storage:grant -- --node NODE_UUID --access read --collection COLLECTION_ID --out /private/path/read-grant.key
agent/bin/mesh-agent storage download --server http://127.0.0.1:7332 --key-file /private/path/read-grant.key --id COLLECTION_ID --destination /absolute/path/to/new-copy
npm run storage:grant -- --node NODE_UUID --access list --out /private/path/list-grant.key
agent/bin/mesh-agent storage list --server http://127.0.0.1:7332 --key-file /private/path/list-grant.key
```

Use existing private parent directories for grant files. Keep them outside the repository. In a remote deployment, provision grants through the controller API over HTTPS; the local helper is not a gateway or certificate installer.

## Evidence and remaining work

Tests exercise real PostgreSQL, the compiled enrolled agent, operator helper, multi-chunk upload and verified download, scope denial, controller stop/restart and revocation. Further tests cover grant/node expiry, issuance versus revocation concurrency, rejection on every storage route, and malformed authorization responses.

The controller currently owns transfer authorization, not a catalogue of collection manifests or transfer progress. Next: central collection metadata and gateway routing, then actual Windows service/ACL/reboot tests. Accounts, operator-key rotation, disk quotas, grant renewal UX and active-stream cancellation remain separate work.
