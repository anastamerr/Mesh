import { Pool } from 'pg';
import { createHash, randomUUID } from 'node:crypto';
import { transaction } from './connection';
import type { CreateWorkload, DesiredState, ExecutionEnvironmentReport, WorkloadObservation } from '../workloads/contracts';
import type { ApplicationRelayTicket, ExecutionEnvironmentRecord, WorkloadCreation, WorkloadRecord,
  WorkloadRepository } from '../workloads/repository';

const workloadColumns = `w.id,w.node_id AS "nodeId",w.execution_environment_id AS "executionEnvironmentId",w.name,w.kind,w.image,w.command,
  jsonb_build_object('cpuMillis',w.cpu_millis,'memoryBytes',w.memory_bytes::float8) AS resources,
  w.input_collection_id AS "inputCollectionId",w.service_port AS "servicePort",
  w.desired_state AS "desiredState",w.revision::float8 AS revision,
  w.observed_revision::float8 AS "observedRevision",w.observed_state AS "observedState",
  w.exit_code AS "exitCode",w.failure_code AS "failureCode",w.output_collection_id AS "outputCollectionId",
  w.created_at AS "createdAt",
  w.updated_at AS "updatedAt",w.observed_at AS "observedAt"`;

function specificationHash(input: CreateWorkload): string {
  const canonical = [input.id, input.nodeId, input.name, input.kind, input.image, input.command,
    input.resources.cpuMillis, input.resources.memoryBytes, input.inputCollectionId, input.servicePort, input.desiredState];
  return createHash('sha256').update(JSON.stringify(canonical)).digest('hex');
}

export class PostgresWorkloadRepository implements WorkloadRepository {
  constructor(private readonly pool: Pool) {}

  async reportExecutionEnvironment(nodeId: string, nodeHash: string,
    input: ExecutionEnvironmentReport): Promise<ExecutionEnvironmentRecord | null> {
    const result = await this.pool.query<ExecutionEnvironmentRecord>(`INSERT INTO execution_environments AS e
      (id,node_id,kind,architecture,status,runtime_version,last_seen_at)
      SELECT $3,n.id,$4,$5,$6,$7,clock_timestamp() FROM nodes n WHERE n.id=$1 AND n.credential_hash=$2
        AND n.revoked_at IS NULL AND n.credential_expires_at>now()
      ON CONFLICT(node_id,kind) DO UPDATE SET architecture=EXCLUDED.architecture,status=EXCLUDED.status,
        runtime_version=EXCLUDED.runtime_version,last_seen_at=EXCLUDED.last_seen_at
      RETURNING e.id,e.node_id AS "nodeId",e.kind,e.architecture,e.status,
        e.runtime_version AS "runtimeVersion",e.last_seen_at AS "lastSeenAt",e.created_at AS "createdAt"`,
    [nodeId, nodeHash, randomUUID(), input.kind, input.architecture, input.status, input.runtimeVersion]);
    return result.rows[0] ?? null;
  }

  async listExecutionEnvironments(nodeId: string): Promise<ExecutionEnvironmentRecord[]> {
    return (await this.pool.query<ExecutionEnvironmentRecord>(`SELECT e.id,e.node_id AS "nodeId",e.kind,e.architecture,
      e.status,e.runtime_version AS "runtimeVersion",e.last_seen_at AS "lastSeenAt",e.created_at AS "createdAt"
      FROM execution_environments e WHERE e.node_id=$1 ORDER BY e.kind,e.id`, [nodeId])).rows;
  }

  async createWorkload(input: CreateWorkload): Promise<WorkloadCreation> {
    const creationHash = specificationHash(input);
    return transaction(this.pool, async client => {
      const inserted = await client.query<WorkloadRecord>(`INSERT INTO workloads AS w
        (id,node_id,execution_environment_id,name,kind,image,command,cpu_millis,memory_bytes,input_collection_id,service_port,desired_state,creation_hash)
        SELECT $1,$2,e.id,$3,$4,$5,$6::jsonb,$7,$8,$9,$10,$11,$12 FROM
        (SELECT id FROM nodes WHERE id=$2 AND revoked_at IS NULL AND credential_expires_at>now() FOR UPDATE) n
        JOIN execution_environments e ON e.node_id=n.id AND e.kind='docker-linux' AND e.status='ready'
        WHERE ($9::text IS NULL OR EXISTS (SELECT 1 FROM collections c
          WHERE c.node_id=n.id AND c.id=$9 AND c.confirmed_at IS NOT NULL))
        AND (SELECT count(*) FROM workloads existing WHERE existing.node_id=n.id AND
          (existing.kind='application' OR existing.observed_state NOT IN ('succeeded','failed')))<100
        ON CONFLICT(id) DO NOTHING RETURNING ${workloadColumns}`,
      [input.id, input.nodeId, input.name, input.kind, input.image, JSON.stringify(input.command),
        input.resources.cpuMillis, input.resources.memoryBytes, input.inputCollectionId,
        input.servicePort, input.desiredState, creationHash]);
      const createdWorkload = inserted.rows[0];
      if (createdWorkload) {
        await client.query("INSERT INTO audit_events(action,node_id) VALUES('workload.created',$1)", [input.nodeId]);
        return { kind: 'created', workload: createdWorkload };
      }
      const existing = (await client.query<WorkloadRecord & { creationHash: string }>(`SELECT ${workloadColumns},
        w.creation_hash AS "creationHash" FROM workloads w WHERE w.id=$1`, [input.id])).rows[0];
      if (!existing) return { kind: 'unavailable' };
      const { creationHash: storedHash, ...existingWorkload } = existing;
      return storedHash === creationHash ? { kind: 'existing', workload: existingWorkload } : { kind: 'conflict' };
    });
  }

  async listWorkloads(): Promise<WorkloadRecord[]> {
    return (await this.pool.query<WorkloadRecord>(`SELECT ${workloadColumns} FROM workloads w
      ORDER BY w.updated_at DESC,w.id DESC LIMIT 100`)).rows;
  }

  async setWorkloadState(id: string, desiredState: DesiredState): Promise<WorkloadRecord | 'invalid-state' | null> {
    return transaction(this.pool, async client => {
      const selected = await client.query<WorkloadRecord>(`SELECT ${workloadColumns} FROM workloads w WHERE w.id=$1 FOR UPDATE`, [id]);
      const current = selected.rows[0];
      if (!current) return null;
      if (current.kind === 'job' && desiredState === 'stopped') return 'invalid-state';
      if (current.desiredState === desiredState) return current;
      const changed = await client.query<WorkloadRecord>(`UPDATE workloads w SET desired_state=$2,
        revision=revision+1,updated_at=clock_timestamp(),observed_revision=NULL,observed_state='pending',
        exit_code=NULL,failure_code=NULL,output_collection_id=NULL,observed_at=NULL
        WHERE w.id=$1 RETURNING ${workloadColumns}`, [id, desiredState]);
      await client.query("INSERT INTO audit_events(action,node_id) VALUES('workload.desired-state.changed',$1)", [current.nodeId]);
      return changed.rows[0]!;
    });
  }

  async assignments(nodeId: string, nodeHash: string): Promise<WorkloadRecord[] | null> {
    return transaction(this.pool, async client => {
      const node = await client.query(`SELECT id FROM nodes WHERE id=$1 AND credential_hash=$2
        AND revoked_at IS NULL AND credential_expires_at>now() FOR SHARE`, [nodeId, nodeHash]);
      if (!node.rowCount) return null;
      return (await client.query<WorkloadRecord>(`SELECT ${workloadColumns} FROM workloads w
        WHERE w.node_id=$1 AND NOT (w.kind='job' AND w.observed_state IN ('succeeded','failed'))
        ORDER BY w.created_at,w.id LIMIT 100`, [nodeId])).rows;
    });
  }

  async observeWorkload(nodeId: string, nodeHash: string, workloadId: string,
    input: WorkloadObservation): Promise<'accepted' | 'stale' | 'unauthorized'> {
    const updated = await this.pool.query(`WITH changed AS (UPDATE workloads w SET observed_revision=$4,
      observed_state=$5,exit_code=$6,failure_code=$7,output_collection_id=$8,observed_at=clock_timestamp()
      FROM nodes n WHERE w.id=$3 AND w.node_id=$1 AND n.id=w.node_id AND n.credential_hash=$2
      AND n.revoked_at IS NULL AND n.credential_expires_at>now() AND w.revision=$4
      AND ($8::text IS NULL OR EXISTS (SELECT 1 FROM collections c WHERE c.node_id=w.node_id
        AND c.id=$8 AND c.confirmed_at IS NOT NULL))
      AND (w.observed_revision IS NULL OR w.observed_revision<$4 OR (w.observed_revision=$4 AND
        ((w.observed_state NOT IN ('succeeded','failed','stopped') AND
          CASE $5 WHEN 'pulling' THEN 1 WHEN 'starting' THEN 2 WHEN 'running' THEN 3 WHEN 'exporting' THEN 4 ELSE 5 END >=
          CASE w.observed_state WHEN 'pending' THEN 0 WHEN 'pulling' THEN 1 WHEN 'starting' THEN 2 WHEN 'running' THEN 3 WHEN 'exporting' THEN 4 ELSE 5 END)
        OR (w.observed_state=$5 AND w.exit_code IS NOT DISTINCT FROM $6 AND w.failure_code IS NOT DISTINCT FROM $7
          AND w.output_collection_id IS NOT DISTINCT FROM $8))))
      RETURNING w.id,w.node_id,w.name,w.output_collection_id), renamed AS (
        UPDATE collections c SET name=left(changed.name || ' output',100) FROM changed
        WHERE c.node_id=changed.node_id AND c.id=changed.output_collection_id AND c.name=c.id RETURNING c.id)
      SELECT id FROM changed`,
    [nodeId, nodeHash, workloadId, input.revision, input.state, input.exitCode, input.failureCode, input.outputCollectionId]);
    if (updated.rowCount) return 'accepted';
    const authorized = await this.pool.query(`SELECT w.id FROM workloads w JOIN nodes n ON n.id=w.node_id
      WHERE w.id=$3 AND w.node_id=$1 AND n.credential_hash=$2 AND n.revoked_at IS NULL
      AND n.credential_expires_at>now()`, [nodeId, nodeHash, workloadId]);
    return authorized.rowCount ? 'stale' : 'unauthorized';
  }

  async createApplicationDeviceTicket(nodeId: string, nodeHash: string, workloadId: string,
    ticketHash: string): Promise<ApplicationRelayTicket | null> {
    return this.createApplicationTicket(workloadId, ticketHash, 'device', nodeId, nodeHash);
  }

  async createApplicationConsumerTicket(workloadId: string, ticketHash: string): Promise<ApplicationRelayTicket | null> {
    return this.createApplicationTicket(workloadId, ticketHash, 'consumer', null, null);
  }

  private async createApplicationTicket(workloadId: string, ticketHash: string, role: 'device' | 'consumer',
    nodeId: string | null, nodeHash: string | null): Promise<ApplicationRelayTicket | null> {
    const result = await this.pool.query<ApplicationRelayTicket>(`WITH eligible AS (
      SELECT w.id AS workload_id,w.node_id,w.service_port,n.public_key_fingerprint,
        LEAST(n.credential_expires_at,now()+interval '10 minutes') AS expires_at
      FROM workloads w JOIN nodes n ON n.id=w.node_id
      WHERE w.id=$1 AND w.kind='application' AND w.desired_state='running' AND w.observed_state='running'
        AND w.observed_revision=w.revision AND n.public_key_fingerprint IS NOT NULL
        AND n.revoked_at IS NULL AND n.credential_expires_at>now()
        AND ($4::uuid IS NULL OR (n.id=$4 AND n.credential_hash=$5))
    ), expired AS (
      DELETE FROM relay_tickets WHERE expires_at<=now() AND EXISTS (SELECT 1 FROM eligible)
    ) INSERT INTO relay_tickets(token_hash,node_id,role,route,workload_id,expires_at)
      SELECT $2,node_id,$3,'app-' || workload_id::text,workload_id,expires_at FROM eligible
      RETURNING node_id AS "nodeId",route,(SELECT service_port FROM eligible)::integer AS "servicePort",
        (SELECT public_key_fingerprint FROM eligible) AS "publicKeyFingerprint",expires_at AS "expiresAt"`,
    [workloadId, ticketHash, role, nodeId, nodeHash]);
    return result.rows[0] ?? null;
  }
}
