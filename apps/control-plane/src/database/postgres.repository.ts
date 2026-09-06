import { StoragePermission } from '../storage/contracts';
import { StorageGrantRepository } from '../storage/repository';
import { randomUUID } from 'node:crypto';
import { Pool } from 'pg';
import { transaction } from './connection';
import { Enrollment, Heartbeat } from '../nodes/contracts';
import { NodeRecord, NodeRepository } from '../nodes/repository';

const publicColumns = `id, name, platform, architecture, agent_version AS "agentVersion",
  created_at AS "createdAt", last_seen_at AS "lastSeenAt", revoked_at AS "revokedAt", inventory`;

export class PostgresNodeRepository implements NodeRepository, StorageGrantRepository {
  constructor(private readonly pool: Pool) {}

  async createEnrollment(hash: string, expiresAt: Date): Promise<void> {
    await transaction(this.pool, async client => {
      await client.query('DELETE FROM enrollment_tokens WHERE expires_at <= now()');
      await client.query('INSERT INTO enrollment_tokens (token_hash, expires_at) VALUES ($1, $2)', [hash, expiresAt]);
      await client.query("INSERT INTO audit_events (action) VALUES ('enrollment.created')");
    });
  }

  async enroll(input: Enrollment, enrollmentHash: string, credentialHash: string,
    credentialExpiresAt: Date): Promise<NodeRecord | null> {
    return transaction(this.pool, async client => {
      // DELETE locks the token row: concurrent requests cannot consume it twice.
      // A failed node insertion rolls back consumption as well.
      const consumed = await client.query(`DELETE FROM enrollment_tokens
        WHERE token_hash = $1 AND expires_at > now() RETURNING token_hash`, [enrollmentHash]);
      if (!consumed.rowCount) return null;
      const result = await client.query<NodeRecord>(`INSERT INTO nodes
        (id, name, platform, architecture, agent_version, credential_hash, credential_expires_at)
        VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING ${publicColumns}`,
        [randomUUID(), input.name, input.platform, input.architecture, input.agentVersion, credentialHash, credentialExpiresAt]);
      const node = result.rows[0]!;
      await client.query("INSERT INTO audit_events (action, node_id) VALUES ('node.enrolled', $1)", [node.id]);
      return node;
    });
  }

  async heartbeat(nodeId: string, credentialHash: string, input: Heartbeat): Promise<'accepted' | 'stale' | 'unauthorized'> {
    // UPDATE acquires the row lock and rechecks its predicate after a concurrent
    // update. The normal path needs one round trip, without weakening sequence
    // or revocation enforcement. Rejected requests never refresh presence.
    const updated = await this.pool.query(`UPDATE nodes SET last_seen_at = clock_timestamp(),
      heartbeat_sequence = $3, inventory = $4::jsonb
      WHERE id = $1 AND credential_hash = $2 AND revoked_at IS NULL
        AND credential_expires_at > now() AND heartbeat_sequence < $3 RETURNING id`,
      [nodeId, credentialHash, input.sequence, JSON.stringify(input.inventory)]);
    if (updated.rowCount) return 'accepted';
    const authorized = await this.pool.query(`SELECT id FROM nodes WHERE id = $1 AND credential_hash = $2
      AND revoked_at IS NULL AND credential_expires_at > now()`, [nodeId, credentialHash]);
    return authorized.rowCount ? 'stale' : 'unauthorized';
  }

  async list(): Promise<NodeRecord[]> {
    return (await this.pool.query<NodeRecord>(`SELECT ${publicColumns} FROM nodes ORDER BY created_at DESC, id DESC LIMIT 100`)).rows;
  }

  async revoke(nodeId: string): Promise<boolean> {
    return transaction(this.pool, async client => {
      const result = await client.query('UPDATE nodes SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL RETURNING id', [nodeId]);
      if (result.rowCount) {
        await client.query("INSERT INTO audit_events (action, node_id) VALUES ('node.revoked', $1)", [nodeId]);
        return true;
      }
      return Boolean((await client.query('SELECT id FROM nodes WHERE id = $1', [nodeId])).rowCount);
    });
  }

  async createStorageGrant(nodeId: string, hash: string, permission: StoragePermission, expiresAt: Date): Promise<boolean> {
    return transaction(this.pool, async client => {
      // Serialize issuance against revocation; validation still checks current node state.
      const node = await client.query(`SELECT id FROM nodes WHERE id=$1 AND revoked_at IS NULL
        AND credential_expires_at > now() FOR SHARE`, [nodeId]);
      if (!node.rowCount) return false;
      await client.query('DELETE FROM storage_grants WHERE expires_at <= now()');
      await client.query(`INSERT INTO storage_grants(token_hash, node_id, access, collection_id, expires_at)
        VALUES ($1,$2,$3,$4,$5)`, [hash, nodeId, permission.access, permission.collectionId, expiresAt]);
      await client.query("INSERT INTO audit_events(action, node_id) VALUES ('storage.grant.created', $1)", [nodeId]);
      return true;
    });
  }

  async validateStorageGrant(nodeId: string, nodeHash: string, grantHash: string, permission: StoragePermission): Promise<boolean> {
    const result = await this.pool.query(`SELECT g.token_hash FROM storage_grants g JOIN nodes n ON n.id=g.node_id
      WHERE n.id=$1 AND n.credential_hash=$2 AND n.revoked_at IS NULL AND n.credential_expires_at > now()
        AND g.token_hash=$3 AND g.expires_at > now() AND g.access=$4 AND g.collection_id IS NOT DISTINCT FROM $5`,
      [nodeId, nodeHash, grantHash, permission.access, permission.collectionId]);
    return Boolean(result.rowCount);
  }

  async ready(): Promise<boolean> {
    try {
      await this.pool.query('SELECT n.id, n.credential_hash, n.inventory, g.token_hash FROM nodes n LEFT JOIN storage_grants g ON g.node_id=n.id LIMIT 0');
      return true;
    } catch { return false; }
  }

  async onApplicationShutdown(): Promise<void> { await this.pool.end(); }
}
