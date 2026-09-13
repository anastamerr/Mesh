import { strict as assert } from 'node:assert';
import { randomUUID } from 'node:crypto';
import { readFile, readdir } from 'node:fs/promises';
import { resolve } from 'node:path';
import { test } from 'node:test';
import { setTimeout as delay } from 'node:timers/promises';
import { Pool } from 'pg';
import { PostgresNodeRepository } from '../src/database/postgres.repository';
import { PostgresWorkloadRepository } from '../src/database/postgres.workload-repository';

// Opt in with a dedicated test database. Each run creates and removes its own
// schema; it never truncates existing tables. Account needs CREATE SCHEMA.
test('PostgreSQL serializes enrollment, heartbeats, workload admission and revocation', {
  skip: !process.env.MESH_TEST_DATABASE_URL,
}, async () => {
  const connectionString = process.env.MESH_TEST_DATABASE_URL;
  const schema = `mesh_test_${randomUUID().replaceAll('-', '')}`;
  const admin = new Pool({ connectionString });
  let pool: Pool | undefined;
  try {
    await admin.query(`CREATE SCHEMA ${schema}`);
    pool = new Pool({ connectionString, options: `-c search_path=${schema}`, application_name: schema, max: 5 });
    for (const name of (await readdir(resolve(__dirname, '../migrations'))).filter(name => name.endsWith('.sql')).sort()) {
      await pool.query(await readFile(resolve(__dirname, '../migrations', name), 'utf8'));
    }
    const repository = new PostgresNodeRepository(pool);
    const workloads = new PostgresWorkloadRepository(pool);
    await repository.createEnrollment('enrollment-hash', new Date(Date.now() + 60_000));
    const input = { enrollmentToken: 'unused', name: 'Lenovo', platform: 'windows' as const,
      architecture: 'amd64' as const, agentVersion: 'dev' };
    const results = await Promise.all([
      repository.enroll(input, 'enrollment-hash', 'credential-a', new Date(Date.now() + 60_000)),
      repository.enroll(input, 'enrollment-hash', 'credential-b', new Date(Date.now() + 60_000)),
    ]);
    assert.equal(results.filter(Boolean).length, 1);
    const index = results.findIndex(Boolean);
    const node = results[index]!;
    const hash = index === 0 ? 'credential-a' : 'credential-b';
    const heartbeat = { sequence: 0, inventory: { cpuLogicalCores: 8, memoryTotalBytes: 16, memoryAvailableBytes: 8 },
      directCandidates: [{ transport: 'tcp' as const, host: '192.168.1.20', port: 7332 }] };
    const beats = await Promise.all([repository.heartbeat(node.id, hash, heartbeat), repository.heartbeat(node.id, hash, heartbeat)]);
    assert.deepEqual(beats.sort(), ['accepted', 'stale']);
    const before = await pool.query('SELECT last_seen_at, inventory FROM nodes WHERE id = $1', [node.id]);
    await pool.query('UPDATE nodes SET public_key_fingerprint=$2 WHERE id=$1', [node.id, 'a'.repeat(64)]);
    const connection = await repository.getNodeConnection(node.id);
    assert.deepEqual(connection?.directCandidates, heartbeat.directCandidates);
    assert.ok(connection?.candidatesObservedAt);
    assert.equal(await repository.heartbeat(node.id, hash, heartbeat), 'stale');
    const after = await pool.query('SELECT last_seen_at, inventory FROM nodes WHERE id = $1', [node.id]);
    assert.deepEqual(after.rows, before.rows, 'stale observations must not refresh presence or inventory');
    // A concurrent higher sequence must win regardless of request ordering.
    await Promise.all([1, 8, 3, 6].map(sequence => repository.heartbeat(node.id, hash, { ...heartbeat, sequence })));
    assert.equal((await pool.query('SELECT heartbeat_sequence FROM nodes WHERE id = $1', [node.id])).rows[0].heartbeat_sequence, '8');
    assert.equal(await repository.heartbeat(node.id, 'wrong', { ...heartbeat, sequence: 1 }), 'unauthorized');
    const permission = { access: 'write' as const, collectionId: 'a'.repeat(64) };
    assert.equal(await repository.createStorageGrant(node.id, 'grant-hash', permission, new Date(Date.now() + 60_000)), true);
    assert.equal(await repository.validateStorageGrant(node.id, hash, 'grant-hash', permission), true);
    assert.equal(await repository.validateStorageGrant(node.id, 'wrong', 'grant-hash', permission), false);
    assert.equal(await repository.validateStorageGrant(node.id, hash, 'grant-hash', { access: 'read', collectionId: permission.collectionId }), false);
    const workload = { id: randomUUID(), nodeId: node.id, name: 'Integration job', kind: 'job' as const,
      image: `example/job@sha256:${'a'.repeat(64)}`, command: ['run'],
      resources: { cpuMillis: 500, memoryBytes: 128 * 1024 * 1024 }, inputCollectionId: null,
      desiredState: 'running' as const, servicePort: null };
    const environment = await workloads.reportExecutionEnvironment(node.id, hash,
      { kind: 'docker-linux', architecture: 'amd64', status: 'ready', runtimeVersion: '28.1.0' });
    assert.ok(environment);
    assert.equal((await workloads.createWorkload(workload)).kind, 'created');
    assert.equal((await workloads.createWorkload(workload)).kind, 'existing');
    assert.equal((await workloads.createWorkload({ ...workload, name: 'Collision' })).kind, 'conflict');
    assert.equal((await workloads.assignments(node.id, hash))?.length, 1);
    const pulling = { revision: 1, state: 'pulling' as const, exitCode: null, failureCode: null, outputCollectionId: null };
    assert.equal(await workloads.observeWorkload(node.id, hash, workload.id, { ...pulling, revision: 2 }), 'stale');
    assert.equal(await workloads.observeWorkload(node.id, hash, workload.id, pulling), 'accepted');
    const outputCollectionId = 'b'.repeat(64);
    assert.equal(await repository.confirmCollection(node.id, hash,
      { id: outputCollectionId, fileCount: 1, totalBytes: 42 }), true);
    assert.equal(await workloads.observeWorkload(node.id, hash, workload.id,
      { revision: 1, state: 'succeeded', exitCode: 0, failureCode: null, outputCollectionId }), 'accepted');
    assert.equal((await repository.listCollections(node.id, '')).find(item => item.id === outputCollectionId)?.name,
      'Integration job output');
    assert.equal((await workloads.assignments(node.id, hash))?.length, 0);
    assert.equal(await workloads.observeWorkload(node.id, hash, workload.id, pulling), 'stale');
    const application = { ...workload, id: randomUUID(), name: 'Integration application', kind: 'application' as const,
      desiredState: 'stopped' as const, servicePort: 8080 };
    assert.equal((await workloads.createWorkload(application)).kind, 'created');
    const started = await workloads.setWorkloadState(application.id, 'running');
    assert.ok(started && started !== 'invalid-state');
    assert.equal(started.desiredState, 'running');
    assert.equal(started.revision, 2);
    const replayed = await workloads.createWorkload(application);
    assert.equal(replayed.kind, 'existing');
    if (replayed.kind === 'existing') assert.equal(replayed.workload.revision, 2);
    await pool.query('UPDATE nodes SET public_key_fingerprint=$2 WHERE id=$1', [node.id, 'c'.repeat(64)]);
    assert.equal(await workloads.observeWorkload(node.id, hash, application.id,
      { revision: 2, state: 'running', exitCode: null, failureCode: null, outputCollectionId: null }), 'accepted');
    const deviceTicketHash = 'd'.repeat(64), consumerTicketHash = 'e'.repeat(64);
    const deviceTicket = await workloads.createApplicationDeviceTicket(node.id, hash, application.id, deviceTicketHash);
    const consumerTicket = await workloads.createApplicationConsumerTicket(application.id, consumerTicketHash);
    assert.equal(deviceTicket?.route, `app-${application.id}`);
    assert.equal(consumerTicket?.servicePort, 8080);
    assert.ok(await repository.authorizeRelay('device', node.id, deviceTicket!.route, deviceTicketHash));
    assert.ok(await repository.authorizeRelay('consumer', node.id, consumerTicket!.route, consumerTicketHash));
    await workloads.setWorkloadState(application.id, 'stopped');
    assert.equal(await repository.authorizeRelay('consumer', node.id, consumerTicket!.route, consumerTicketHash), null);
    // One application already occupies a slot; leave exactly one of 100 free.
    for (let index = 0; index < 98; index++) {
      assert.equal((await workloads.createWorkload({ ...workload, id: randomUUID() })).kind, 'created');
    }
    const admissionLock = await pool.connect();
    let admissions: Promise<Awaited<ReturnType<typeof workloads.createWorkload>>[]> | undefined;
    try {
      await admissionLock.query('BEGIN');
      await admissionLock.query('SELECT id FROM nodes WHERE id=$1 FOR UPDATE', [node.id]);
      admissions = Promise.all(Array.from({ length: 4 }, () => workloads.createWorkload({ ...workload, id: randomUUID() })));
      // Force every INSERT/lock contender to start before releasing the lock.
      // This catches counting capacity using a snapshot taken before waiting.
      let waiting = 0;
      for (let attempt = 0; attempt < 100 && waiting < 4; attempt++) {
        const blocked = await admin.query<{ count: string }>(`SELECT count(*) FROM pg_stat_activity
          WHERE application_name=$1 AND wait_event_type='Lock'`, [schema]);
        waiting = Number(blocked.rows[0]!.count);
        if (waiting < 4) await delay(20);
      }
      assert.equal(waiting, 4, 'all admission contenders reached the lock');
    } finally {
      await admissionLock.query('ROLLBACK');
      admissionLock.release();
    }
    assert.ok(admissions);
    const admitted = await admissions;
    assert.equal(admitted.filter(result => result.kind === 'created').length, 1);
    assert.equal(admitted.filter(result => result.kind === 'unavailable').length, 3);
    assert.equal((await workloads.assignments(node.id, hash))?.length, 100);
    await pool.query("UPDATE storage_grants SET expires_at=now()-interval '1 second' WHERE token_hash='grant-hash'");
    assert.equal(await repository.validateStorageGrant(node.id, hash, 'grant-hash', permission), false);
    // Issuance may win the lock first, but no grant validates after revocation commits.
    await Promise.all([
      repository.createStorageGrant(node.id, 'racing-grant', permission, new Date(Date.now() + 60_000)),
      repository.revoke(node.id),
    ]);
    assert.equal(await repository.validateStorageGrant(node.id, hash, 'racing-grant', permission), false);
    assert.equal(await repository.createStorageGrant(node.id, 'after-revoke', permission, new Date(Date.now() + 60_000)), false);
    await Promise.all([repository.revoke(node.id), repository.revoke(node.id)]);
    assert.equal((await pool.query("SELECT count(*) FROM audit_events WHERE action = 'node.revoked' AND node_id = $1", [node.id])).rows[0].count, '1');
    assert.equal(await repository.heartbeat(node.id, hash, { ...heartbeat, sequence: 1 }), 'unauthorized');
    assert.equal((await repository.list()).length, 1);
    assert.equal(await repository.ready(), true);
    // Failed node insertion must roll back token consumption.
    await repository.createEnrollment('rollback-token', new Date(Date.now() + 60_000));
    await assert.rejects(repository.enroll(input, 'rollback-token', hash, new Date(Date.now() + 60_000)));
    const replacement = await repository.enroll(input, 'rollback-token', 'new-credential', new Date(Date.now() + 60_000));
    assert.ok(replacement);
    await Promise.all([
      repository.heartbeat(replacement.id, 'new-credential', heartbeat),
      repository.revoke(replacement.id),
    ]);
    assert.equal(await repository.heartbeat(replacement.id, 'new-credential', { ...heartbeat, sequence: 1 }), 'unauthorized');
  } finally {
    await pool?.end();
    await admin.query(`DROP SCHEMA IF EXISTS ${schema} CASCADE`);
    await admin.end();
  }
});
