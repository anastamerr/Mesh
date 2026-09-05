import { randomUUID } from 'node:crypto';
import { Pool, PoolClient } from 'pg';
import { Enrollment, Heartbeat } from '../nodes/contracts';
import { NodeRecord, NodeRepository } from '../nodes/repository';

const publicColumns = `id, name, platform, architecture, agent_version AS "agentVersion",
  created_at AS "createdAt", last_seen_at AS "lastSeenAt", revoked_at AS "revokedAt", inventory`;

export class PostgresNodeRepository implements NodeRepository {
  constructor(private readonly pool: Pool) {}

  private async transaction<T>(fn: (client: PoolClient) => Promise<T>): Promise<T> {
    const client = await this.pool.connect();
    try {
      await client.query('BEGIN');
      const result = await fn(client);
      await client.query('COMMIT');
      return result;
    } catch (error) {
      await client.query('ROLLBACK');
      throw error;
    } finally { client.release(); }
  }

  async createEnrollment(hash: string, expiresAt: Date): Promise<void> {
    await this.transaction(async client => {
      await client.query('DELETE FROM enrollment_tokens WHERE expires_at <= now()');
      await client.query('INSERT INTO enrollment_tokens (token_hash, expires_at) VALUES ($1, $2)', [hash, expiresAt]);
      await client.query("INSERT INTO audit_events (action) VALUES ('enrollment.created')");
    });
  }

  async enroll(input: Enrollment, enrollmentHash: string, credentialHash: string,
    credentialExpiresAt: Date): Promise<NodeRecord | null> {
    return this.transaction(async client => {
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
    return this.transaction(async client => {
      // Serializes heartbeat checks with both revocation and other heartbeats.
      const result = await client.query<{ heartbeat_sequence: string }>(`SELECT heartbeat_sequence FROM nodes
        WHERE id = $1 AND credential_hash = $2 AND revoked_at IS NULL AND credential_expires_at > now()
        FOR UPDATE`, [nodeId, credentialHash]);
      const node = result.rows[0];
      if (!node) return 'unauthorized';
      if (input.sequence <= Number(node.heartbeat_sequence)) return 'stale';
      await client.query(`UPDATE nodes SET last_seen_at = clock_timestamp(), heartbeat_sequence = $2,
        inventory = $3::jsonb WHERE id = $1`, [nodeId, input.sequence, JSON.stringify(input.inventory)]);
      return 'accepted';
    });
  }

  async list(): Promise<NodeRecord[]> {
    return (await this.pool.query<NodeRecord>(`SELECT ${publicColumns} FROM nodes ORDER BY created_at DESC LIMIT 100`)).rows;
  }

  async revoke(nodeId: string): Promise<boolean> {
    return this.transaction(async client => {
      const result = await client.query('UPDATE nodes SET revoked_at = COALESCE(revoked_at, now()) WHERE id = $1 RETURNING id', [nodeId]);
      if (!result.rowCount) return false;
      await client.query("INSERT INTO audit_events (action, node_id) VALUES ('node.revoked', $1)", [nodeId]);
      return true;
    });
  }

  async ready(): Promise<boolean> {
    try {
      await this.pool.query('SELECT id, credential_hash, inventory FROM nodes LIMIT 0');
      return true;
    } catch { return false; }
  }

  async onApplicationShutdown(): Promise<void> { await this.pool.end(); }
}
