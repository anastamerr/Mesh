import { strict as assert } from 'node:assert';
import { randomUUID } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { test } from 'node:test';
import { Pool } from 'pg';
import { PostgresNodeRepository } from '../src/database/postgres.repository';

// Opt in with a dedicated test database. Each run creates and removes its own
// schema; it never truncates existing tables. Account needs CREATE SCHEMA.
test('PostgreSQL atomically consumes enrollment and serializes heartbeats/revocation', {
  skip: !process.env.MESH_TEST_DATABASE_URL,
}, async () => {
  const connectionString = process.env.MESH_TEST_DATABASE_URL;
  const schema = `mesh_test_${randomUUID().replaceAll('-', '')}`;
  const admin = new Pool({ connectionString });
  let pool: Pool | undefined;
  try {
    await admin.query(`CREATE SCHEMA ${schema}`);
    pool = new Pool({ connectionString, options: `-c search_path=${schema}`, max: 5 });
    await pool.query(await readFile(resolve(__dirname, '../migrations/001_nodes.sql'), 'utf8'));
    const repository = new PostgresNodeRepository(pool);
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
    const heartbeat = { sequence: 0, inventory: { cpuLogicalCores: 8, memoryTotalBytes: 16, memoryAvailableBytes: 8 } };
    const beats = await Promise.all([repository.heartbeat(node.id, hash, heartbeat), repository.heartbeat(node.id, hash, heartbeat)]);
    assert.deepEqual(beats.sort(), ['accepted', 'stale']);
    const before = await pool.query('SELECT last_seen_at, inventory FROM nodes WHERE id = $1', [node.id]);
    assert.equal(await repository.heartbeat(node.id, hash, heartbeat), 'stale');
    const after = await pool.query('SELECT last_seen_at, inventory FROM nodes WHERE id = $1', [node.id]);
    assert.deepEqual(after.rows, before.rows, 'stale observations must not refresh presence or inventory');
    // A concurrent higher sequence must win regardless of request ordering.
    await Promise.all([1, 8, 3, 6].map(sequence => repository.heartbeat(node.id, hash, { ...heartbeat, sequence })));
    assert.equal((await pool.query('SELECT heartbeat_sequence FROM nodes WHERE id = $1', [node.id])).rows[0].heartbeat_sequence, '8');
    assert.equal(await repository.heartbeat(node.id, 'wrong', { ...heartbeat, sequence: 1 }), 'unauthorized');
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
