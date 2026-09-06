# ADR 0003: Native folder copies and a local transfer journal

Status: implemented first storage slice. Authorization is extended by [ADR 0004](0004-storage-authorization.md); the shared-key mode described below is now restricted to loopback.

## Boundary

The native agent owns bulk data and exposes a small HTTP data API. The NestJS control plane continues to own node enrollment and presence; it does not proxy file bytes. `mesh-agent storage serve` currently runs separately from `mesh-agent run` and requires an explicitly selected storage directory.

This slice uses a separate random storage key, read from a private file. It grants full access to this one storage root. It is not the operator API key or a node heartbeat credential. Control-plane revocation therefore does **not** revoke storage access. Stop the storage listener to disable access; replace its key and restart to rotate access. Before remote gateway integration, replace this bootstrap mechanism with controller-issued, short-lived, collection-scoped authorization that honors node revocation.

HTTP is accepted only on loopback. Other listeners require enrolled authorization (ADR 0004), a TLS certificate and private key. Clients verify certificates through the OS trust store and refuse redirects. There is no public tunnel, NAT traversal, account system, or discovery yet.

## Model

A collection is an immutable folder copy. Its ID is the SHA-256 of the canonical version-1 manifest JSON. The manifest contains sorted entries with relative paths, directory markers, sizes, and SHA-256 checksums. Identical manifests resume or return the same collection; changing content creates another collection. Copies preserve file bytes and empty directories, not ownership, modes, timestamps, links, extended attributes, or live filesystem snapshot semantics. Keep the source stable during a transfer.

Paths currently use a conservative ASCII subset compatible with Windows: no traversal, absolute paths, backslashes, device names, alternate streams, trailing dots/spaces, case collisions, or differently spelled parent directories. Unicode filename support needs a deliberate normalization/collision policy and cross-platform tests. Links and special files are rejected. Go `os.Root` confines uploaded and downloaded file access to the particular collection subtree, separate from the journal. This is a filesystem boundary, not a sandbox against the local account that owns the data, mount points, or administrators.

## Layout and persistence

```
<approved-root>/
  .mesh/journal.db      SQLite transfer manifests, offsets and publication state
  .mesh/storage.lock   one serving process per root
  staging/<id>/        incomplete native files, unavailable through download/list
  collections/<id>/    published native files, readable without Mesh
```

Use a dedicated private local directory owned by the agent account. Unix requires 0700 directories; the storage key requires 0600. Windows deployments must restrict directory/key ACLs to that account; automated ACL provisioning and runtime validation remain pending. SQLite is embedded through the pure-Go modernc driver, so users need no SQLite installation or cgo compiler. The journal has a schema version and rejects newer formats. Connection-local synchronization and foreign-key settings are applied to every SQLite connection, including replacements.

Each request carries at most 4 MiB. The server admits four active requests and serializes mutations; there is no speculative concurrent-disk scheduler. Total transient request memory is bounded by these limits plus manifests, JSON decoding and runtime buffers, rather than file size. Manifests are limited to 2 MiB and 10,000 entries, individual files to 1 TiB. These are protocol bounds, not free-space reservations or disk quotas.

For a chunk, the agent checks its offset against SQLite, truncates any uncommitted tail, writes and syncs bytes, then commits the new offset with SQLite FULL synchronization. A lost response is resolved by submitting the same manifest again. A mismatched final checksum resets that file's offset so it can be retransmitted. File verification and downloads stream through bounded buffers.

Publication requires all offsets and checksums to match. The agent syncs each directory once in child-before-parent order, renames the tree from staging to collections, then marks it complete in SQLite. Download completion uses the same directory-sync routine. Retrying finish recovers a crash between rename and journal commit by verifying the published tree. Unix directory entries are synced; Windows directory fsync is unavailable, so sudden power-loss durability still needs platform-specific validation. Abrupt process termination and restart are covered by tests.

Downloads verify the manifest ID and every file checksum. They reserve a new destination directory and remove partial output on an ordinary failure. A killed downloader can leave a partial directory: remove it or choose another destination before retrying. Downloads do not yet resume. Published copies are immutable through the API; editing their native files externally breaks that guarantee and is detected on verified retrieval.

## HTTP contract

All requests require `Authorization: Bearer <storage-key>`. JSON responses return HTTP 200; errors return generic text without credentials or local paths. Invalid input: 400; unauthorized: 401; unpublished/missing collection: 404; offset/integrity conflict: 409; capacity busy: 503. Other I/O failures return 500.

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/v1/collections` | Submit a manifest; return `{id, complete, offsets}` for creation/resumption |
| PUT | `/v1/collections/{id}/files/{index}` | Binary chunk with `Upload-Offset`; return `{offset}` |
| POST | `/v1/collections/{id}/finish` | Verify and publish; return progress with `complete: true` |
| GET | `/v1/collections?after={id}` | Up to 100 published IDs in ascending order; use the last ID for the next page |
| GET | `/v1/collections/{id}` | Published manifest |
| GET | `/v1/collections/{id}/files/{index}` | Stream one published file |

The canonical JSON is compact Go `encoding/json` output: top-level fields `version`, then `entries`; each entry has `path`, `size`, `sha256`, then `directory` only when true. HTML-sensitive ASCII characters use JSON Unicode escapes. No trailing newline enters the hash. Directory entries have size zero and an empty checksum; empty folders use an empty entries array.

The file index is its zero-based ordinal in the manifest, including directory entries. Directory entries cannot be uploaded or downloaded as files. Offsets include directories with value zero. The built-in client exits on failure; rerunning upload resumes from durable progress instead of hiding indefinite retries.

## Next extensions

1. Controller-owned collections and short-lived transfer authorization, replacing the independent bootstrap key.
2. Gateway routing to enrolled agents without incoming home-router ports.
3. Actual Windows TLS/ACL, restart, disk-full, long-path and power-loss tests.
4. Explicit cancel/delete and abandoned-transfer cleanup, storage reservations and disk alerts.
5. Unicode paths, resumable downloads and incremental source scanning after their contracts are defined.
6. Versioned backups and restore, followed by compute jobs that receive explicit collection access.

There is no destructive cleanup API, automatic synchronization, deduplication between different collections, throughput claim, replication, or backup guarantee in this slice.
