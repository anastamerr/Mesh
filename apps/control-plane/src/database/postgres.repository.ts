import { StoragePermission } from '../storage/contracts';
import type { CollectionRegistration, CollectionStatistics } from '../storage/collection.contracts';
import { CollectionRecord, StorageRepository } from '../storage/repository';
import { randomUUID } from 'node:crypto';
import { Pool } from 'pg';
import { transaction } from './connection';
import { Enrollment, Heartbeat } from '../nodes/contracts';
import { NodeRecord, NodeRepository } from '../nodes/repository';
import type { PairingRequest } from '../pairing/contracts';
import type { PairingApproval, PairingChallenge, PairingRepository, PairingStatus, PendingPairing } from '../pairing/repository';

const publicColumns = `id, name, platform, architecture, agent_version AS "agentVersion",
  created_at AS "createdAt", last_seen_at AS "lastSeenAt", revoked_at AS "revokedAt", inventory,
  public_key_fingerprint AS "publicKeyFingerprint"`;

export class PostgresNodeRepository implements NodeRepository, StorageRepository, PairingRepository {
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

  async renew(nodeId: string, credentialHash: string): Promise<Date | null> {
    // An active paired device may extend its existing lease. This does not
    // rotate the bearer or revive expired/revoked identities.
    const result = await this.pool.query<{ expiresAt: Date }>(`UPDATE nodes SET credential_expires_at=
      CASE WHEN credential_expires_at < now()+interval '7 days' THEN now()+interval '30 days'
      ELSE credential_expires_at END WHERE id=$1 AND credential_hash=$2
      AND public_key_fingerprint IS NOT NULL AND revoked_at IS NULL AND credential_expires_at > now()
      RETURNING credential_expires_at AS "expiresAt"`, [nodeId, credentialHash]);
    return result.rows[0]?.expiresAt ?? null;
  }

  async createPairing(input: PairingRequest, id: string, code: string,
    expiresAt: Date): Promise<PairingChallenge | 'capacity' | 'conflict' | 'expired'> {
    return transaction(this.pool, async client => {
      // Serialize the global pending bound with creation. This public endpoint is
      // still expected to sit behind edge rate limiting before internet exposure.
      await client.query('SELECT pg_advisory_xact_lock(68435793)');
      await client.query(`DELETE FROM pairing_challenges p USING nodes n WHERE p.node_id=n.id
        AND (n.credential_expires_at <= now() OR n.revoked_at IS NOT NULL)`);
      await client.query(`DELETE FROM pairing_challenges WHERE approved_at IS NULL
        AND expires_at <= now()-interval '24 hours'`);
      const existing = await client.query<PairingChallenge & PairingRequest & { approvedAt: Date | null }>(`SELECT id,code,expires_at AS "expiresAt",
        request_id AS "pairingRequestId",pairing_secret_hash AS "pairingSecretHash",
        node_credential_hash AS "nodeCredentialHash",public_key_fingerprint AS "publicKeyFingerprint",
        name,platform,architecture,agent_version AS "agentVersion",approved_at AS "approvedAt"
        FROM pairing_challenges WHERE request_id=$1`, [input.pairingRequestId]);
      const previous = existing.rows[0];
      if (previous) {
        const matches = previous.pairingSecretHash === input.pairingSecretHash
          && previous.nodeCredentialHash === input.nodeCredentialHash
          && previous.publicKeyFingerprint === input.publicKeyFingerprint && previous.name === input.name
          && previous.platform === input.platform && previous.architecture === input.architecture
          && previous.agentVersion === input.agentVersion;
        if (!matches) return 'conflict';
        if (!previous.approvedAt && previous.expiresAt.getTime() <= Date.now()) return 'expired';
        return { id: previous.id, code: previous.code, expiresAt: previous.expiresAt };
      }
      const count = await client.query<{ count: string }>(`SELECT count(*) FROM pairing_challenges
        WHERE approved_at IS NULL AND expires_at > now()`);
      if (Number(count.rows[0]!.count) >= 100) return 'capacity';
      const result = await client.query<PairingChallenge>(`INSERT INTO pairing_challenges
        (id,request_id,code,pairing_secret_hash,node_credential_hash,public_key_fingerprint,
          name,platform,architecture,agent_version,expires_at)
        VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
        RETURNING id,code,expires_at AS "expiresAt"`, [id, input.pairingRequestId, code,
        input.pairingSecretHash, input.nodeCredentialHash, input.publicKeyFingerprint, input.name,
        input.platform, input.architecture, input.agentVersion, expiresAt]);
      await client.query("INSERT INTO audit_events(action) VALUES ('pairing.created')");
      return result.rows[0]!;
    });
  }

  async listPendingPairings(): Promise<PendingPairing[]> {
    return (await this.pool.query<PendingPairing>(`SELECT id,code,expires_at AS "expiresAt",name,platform,architecture,
      agent_version AS "agentVersion",public_key_fingerprint AS "publicKeyFingerprint",created_at AS "createdAt"
      FROM pairing_challenges WHERE approved_at IS NULL AND expires_at > now()
      ORDER BY created_at DESC,id DESC LIMIT 100`)).rows;
  }

  async approvePairing(id: string, expectedFingerprint: string, credentialExpiresAt: Date): Promise<PairingApproval> {
    return transaction(this.pool, async client => {
      // Approval volume is tiny; one lock makes fingerprint/credential uniqueness
      // deterministic across challenges instead of leaking a database error.
      await client.query('SELECT pg_advisory_xact_lock(68435792)');
      const selected = await client.query<PairingRequest & { approvedAt: Date | null; expiresAt: Date;
        nodeId: string | null }>(`SELECT request_id AS "pairingRequestId",pairing_secret_hash AS "pairingSecretHash",
        node_credential_hash AS "nodeCredentialHash",public_key_fingerprint AS "publicKeyFingerprint",
        name,platform,architecture,agent_version AS "agentVersion",approved_at AS "approvedAt",
        expires_at AS "expiresAt",node_id AS "nodeId" FROM pairing_challenges WHERE id=$1 FOR UPDATE`, [id]);
      const challenge = selected.rows[0];
      if (!challenge) return { kind: 'missing' };
      if (challenge.publicKeyFingerprint !== expectedFingerprint) return { kind: 'identity-conflict' };
      if (challenge.approvedAt && challenge.nodeId) {
        const existing = await client.query<NodeRecord & { credentialExpiresAt: Date }>(`SELECT ${publicColumns},
          credential_expires_at AS "credentialExpiresAt" FROM nodes WHERE id=$1`, [challenge.nodeId]);
        const record = existing.rows[0];
        if (!record || record.revokedAt || record.credentialExpiresAt.getTime() <= Date.now()) return { kind: 'expired' };
        const { credentialExpiresAt: existingExpiry, ...node } = record;
        return { kind: 'approved', node, credentialExpiresAt: existingExpiry };
      }
      if (challenge.expiresAt.getTime() <= Date.now()) return { kind: 'expired' };
      const collision = await client.query(`SELECT id FROM nodes
        WHERE public_key_fingerprint=$1 OR credential_hash=$2 LIMIT 1`,
        [challenge.publicKeyFingerprint, challenge.nodeCredentialHash]);
      if (collision.rowCount) return { kind: 'identity-conflict' };
      const nodeId = randomUUID();
      const inserted = await client.query<NodeRecord>(`INSERT INTO nodes
        (id,name,platform,architecture,agent_version,credential_hash,credential_expires_at,public_key_fingerprint)
        VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING ${publicColumns}`,
        [nodeId, challenge.name, challenge.platform, challenge.architecture, challenge.agentVersion,
          challenge.nodeCredentialHash, credentialExpiresAt, challenge.publicKeyFingerprint]);
      await client.query(`UPDATE pairing_challenges SET approved_at=clock_timestamp(),node_id=$2 WHERE id=$1`, [id, nodeId]);
      await client.query("INSERT INTO audit_events(action,node_id) VALUES ('node.paired',$1)", [nodeId]);
      return { kind: 'approved', node: inserted.rows[0]!, credentialExpiresAt };
    });
  }

  async getPairingStatus(id: string, pairingSecretHash: string): Promise<PairingStatus> {
    const result = await this.pool.query<NodeRecord & { expiresAt: Date;
      approvedAt: Date | null; credentialExpiresAt: Date | null }>(`SELECT
      p.expires_at AS "expiresAt",p.approved_at AS "approvedAt",n.credential_expires_at AS "credentialExpiresAt",
      ${publicColumns.split(',').map(column => `n.${column.trim()}`).join(',')}
      FROM pairing_challenges p LEFT JOIN nodes n ON n.id=p.node_id WHERE p.id=$1 AND p.pairing_secret_hash=$2`, [id, pairingSecretHash]);
    const challenge = result.rows[0];
    if (!challenge) return { kind: 'unauthorized' };
    if (challenge.approvedAt && challenge.credentialExpiresAt) {
      if (challenge.revokedAt || challenge.credentialExpiresAt.getTime() <= Date.now()) return { kind: 'expired' };
      const { expiresAt: _expiresAt, approvedAt: _approvedAt, credentialExpiresAt, ...node } = challenge;
      return { kind: 'approved', node, credentialExpiresAt };
    }
    return challenge.expiresAt.getTime() <= Date.now() ? { kind: 'expired' } : { kind: 'pending' };
  }

  private async relaySource(role: 'device' | 'consumer', nodeId: string, tokenHash: string):
    Promise<{ subject: string; expiresAt: Date } | null> {
    if (role === 'device') {
      const result = await this.pool.query<{ expiresAt: Date }>(`SELECT credential_expires_at AS "expiresAt" FROM nodes
        WHERE id=$1 AND credential_hash=$2 AND public_key_fingerprint IS NOT NULL
          AND revoked_at IS NULL AND credential_expires_at > now()`, [nodeId, tokenHash]);
      return result.rows[0] ? { subject: `device:${nodeId}`, expiresAt: result.rows[0].expiresAt } : null;
    }
    const result = await this.pool.query<{ expiresAt: Date }>(`SELECT LEAST(g.expires_at,n.credential_expires_at) AS "expiresAt"
      FROM storage_grants g
      JOIN nodes n ON n.id=g.node_id WHERE n.id=$1 AND g.token_hash=$2 AND g.expires_at > now()
        AND n.public_key_fingerprint IS NOT NULL AND n.revoked_at IS NULL
        AND n.credential_expires_at > now() LIMIT 1`, [nodeId, tokenHash]);
    return result.rows[0] ? { subject: `consumer:${nodeId}`, expiresAt: result.rows[0].expiresAt } : null;
  }

  async createRelayTicket(role: 'device' | 'consumer', nodeId: string, sourceHash: string, ticketHash: string): Promise<Date | null> {
    const source = await this.relaySource(role, nodeId, sourceHash);
    if (!source) return null;
    const expiry = new Date(Math.min(source.expiresAt.getTime(), Date.now() + 10 * 60 * 1000));
    await this.pool.query('DELETE FROM relay_tickets WHERE expires_at<=now()');
    await this.pool.query('INSERT INTO relay_tickets(token_hash,node_id,role,expires_at) VALUES($1,$2,$3,$4)', [ticketHash,nodeId,role,expiry]);
    return expiry;
  }

  async authorizeRelay(role: 'device' | 'consumer', nodeId: string, tokenHash: string): Promise<{ subject: string; expiresAt: Date } | null> {
    const result = await this.pool.query<{ expiresAt: Date }>(`SELECT LEAST(t.expires_at,n.credential_expires_at) AS "expiresAt"
      FROM relay_tickets t JOIN nodes n ON n.id=t.node_id WHERE t.token_hash=$1 AND t.node_id=$2 AND t.role=$3
      AND t.expires_at>now() AND n.credential_expires_at>now() AND n.revoked_at IS NULL`, [tokenHash,nodeId,role]);
    return result.rows[0] ? { subject: `${role}:${nodeId}`, expiresAt: result.rows[0].expiresAt } : null;
  }

  async getNodeConnection(nodeId: string): Promise<{ nodeId: string; publicKeyFingerprint: string } | null> {
    const result = await this.pool.query<{ nodeId: string; publicKeyFingerprint: string }>(`SELECT id AS "nodeId",
      public_key_fingerprint AS "publicKeyFingerprint" FROM nodes WHERE id=$1
      AND public_key_fingerprint IS NOT NULL AND revoked_at IS NULL AND credential_expires_at > now()`, [nodeId]);
    return result.rows[0] ?? null;
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

  async registerCollection(nodeId: string, input: CollectionRegistration): Promise<boolean> {
    const result = await this.pool.query(`INSERT INTO collections(node_id,id,name,file_count,total_bytes)
      SELECT id,$2,$3,$4,$5 FROM nodes WHERE id=$1 AND revoked_at IS NULL AND credential_expires_at > now()
      ON CONFLICT(node_id,id) DO UPDATE SET name=EXCLUDED.name RETURNING id`,
      [nodeId, input.id, input.name, input.fileCount, input.totalBytes]);
    return Boolean(result.rowCount);
  }

  async confirmCollection(nodeId: string, nodeHash: string, input: CollectionStatistics): Promise<boolean> {
    const result = await this.pool.query(`INSERT INTO collections(node_id,id,name,file_count,total_bytes,confirmed_at)
      SELECT id,$3,$3,$4,$5,clock_timestamp() FROM nodes WHERE id=$1 AND credential_hash=$2
        AND revoked_at IS NULL AND credential_expires_at > now()
      ON CONFLICT(node_id,id) DO UPDATE SET file_count=EXCLUDED.file_count,total_bytes=EXCLUDED.total_bytes,
        confirmed_at=EXCLUDED.confirmed_at RETURNING id`, [nodeId, nodeHash, input.id, input.fileCount, input.totalBytes]);
    return Boolean(result.rowCount);
  }

  async listCollections(nodeId: string, after: string): Promise<CollectionRecord[]> {
    const result = await this.pool.query<CollectionRecord>(`SELECT id,name,file_count AS "fileCount",
      total_bytes::float8 AS "totalBytes",confirmed_at AS "confirmedAt" FROM collections
      WHERE node_id=$1 AND id>$2 ORDER BY id LIMIT 100`, [nodeId, after]);
    return result.rows;
  }

  async ready(): Promise<boolean> {
    try {
      await this.pool.query('SELECT id FROM collections LIMIT 0');
      await this.pool.query('SELECT n.id, n.credential_hash, n.inventory, g.token_hash FROM nodes n LEFT JOIN storage_grants g ON g.node_id=n.id LIMIT 0');
      await this.pool.query('SELECT pairing_secret_hash, node_credential_hash, public_key_fingerprint FROM pairing_challenges LIMIT 0');
      await this.pool.query('SELECT token_hash FROM relay_tickets LIMIT 0');
      return true;
    } catch { return false; }
  }

  async onApplicationShutdown(): Promise<void> { await this.pool.end(); }
}
