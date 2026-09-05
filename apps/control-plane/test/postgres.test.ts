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
    assert.equal(await repository.heartbeat(node.id, 'wrong', { ...heartbeat, sequence: 1 }), 'unauthorized');
    await repository.revoke(node.id);
    assert.equal(await repository.heartbeat(node.id, hash, { ...heartbeat, sequence: 1 }), 'unauthorized');
    assert.equal((await repository.list()).length, 1);
    assert.equal(await repository.ready(), true);
  } finally {
    await pool?.end();
    await admin.query(`DROP SCHEMA IF EXISTS ${schema} CASCADE`);
    await admin.end();
  }
});
