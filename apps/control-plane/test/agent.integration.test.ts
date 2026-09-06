import { z } from 'zod';
import { storageFlow } from './storage-flow';
import { strict as assert } from 'node:assert';
import { execFile, spawn } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { resolve, join } from 'node:path';
import { test } from 'node:test';
import { promisify } from 'node:util';
import { Pool } from 'pg';
import { createApp } from '../src/app';
import { PostgresNodeRepository } from '../src/database/postgres.repository';

// Runs the real compiled Go executable against HTTP and an isolated PostgreSQL schema.
// Neither credentials nor enrollment tokens are passed on the command line.
test('real agent enrolls, persists sequence across processes/controller restart, and enforces storage grants through outage and revocation', {
  skip: !process.env.MESH_TEST_DATABASE_URL || !process.env.MESH_TEST_AGENT_BINARY,
  timeout: 60_000,
}, async () => {
  const connectionString = process.env.MESH_TEST_DATABASE_URL;
  const binary = resolve(process.env.MESH_TEST_AGENT_BINARY!);
  const schema = `mesh_agent_${randomUUID().replaceAll('-', '')}`;
  const admin = new Pool({ connectionString });
  const directory = await mkdtemp(join(tmpdir(), 'mesh-agent-test-'));
  const key = 'agent_integration_test_operator_key_0123456789';
  const headers = { authorization: `Bearer ${key}` };
  let app: Awaited<ReturnType<typeof createApp>> | undefined;
  let pool: Pool | undefined;
  const command = (args: string[], stdin = '') => new Promise<{ code: number | null; stdout: string; stderr: string }>((resolve, reject) => {
    const child = spawn(binary, [...args, '--state-dir', directory], { stdio: ['pipe', 'pipe', 'pipe'], timeout: 10_000 });
    let stdout = '';
    let stderr = '';
    child.stdout.on('data', data => { stdout += data; });
    child.stderr.on('data', data => { stderr += data; });
    child.once('error', reject);
    child.once('close', code => resolve({ code, stdout, stderr }));
    child.stdin.end(stdin);
  });
  async function start(port = 0) {
    pool = new Pool({ connectionString, options: `-c search_path=${schema}` });
    app = await createApp({ adminKey: key, databaseUrl: 'postgresql://unused', host: '127.0.0.1', port },
      new PostgresNodeRepository(pool));
    await app.listen(port, '127.0.0.1');
    return await app.getUrl();
  }
  try {
    await admin.query(`CREATE SCHEMA ${schema}`);
    const initial = new Pool({ connectionString, options: `-c search_path=${schema}` });
    try {
      await initial.query(await readFile(resolve(__dirname, '../migrations/001_nodes.sql'), 'utf8'));
      await initial.query(await readFile(resolve(__dirname, '../migrations/002_storage_grants.sql'), 'utf8'));
      await initial.query(await readFile(resolve(__dirname, '../migrations/003_collections.sql'), 'utf8'));
    }
    finally { await initial.end(); }
    const url = await start();
    const response = await fetch(`${url}/v1/enrollment-tokens`, { method: 'POST', headers });
    assert.equal(response.status, 201);
    const { enrollmentToken } = z.object({ enrollmentToken: z.string() }).parse(await response.json());
    const enrolled = await command(['enroll', '--server', url, '--name', 'Integration host', '--token-stdin'], enrollmentToken);
    assert.equal(enrolled.code, 0, enrolled.stderr);
    assert.ok(!enrolled.stdout.includes(enrollmentToken));
    for (let i = 0; i < 2; i++) {
      const beat = await command(['heartbeat']);
      assert.equal(beat.code, 0, beat.stderr);
    }
    const status = JSON.parse((await command(['status'])).stdout);
    assert.equal(status.nextSequence, 2);
    assert.equal(status.credential, undefined);
    const nodes = z.array(z.object({ id: z.uuid(), status: z.string(), inventory: z.object({ memoryTotalBytes: z.number() }) }))
      .parse(await (await fetch(`${url}/v1/nodes`, { headers })).json());
    assert.equal(nodes[0]?.id, status.nodeId);
    assert.equal(nodes[0]?.status, 'online');
    assert.ok(nodes[0]!.inventory.memoryTotalBytes > 0);
    const port = Number(new URL(url).port);
    await app!.close();
    app = undefined;
    pool = undefined;
    await start(port);
    const beat = await command(['heartbeat']);
    assert.equal(beat.code, 0, beat.stderr);
    assert.equal(JSON.parse((await command(['status'])).stdout).nextSequence, 3);
    await storageFlow({ binary, directory, nodeId: status.nodeId, url, operatorKey: key,
      whileControllerOffline: async check => {
        await app!.close(); app = undefined; pool = undefined;
        try { await check(); } finally { await start(port); }
      },
    });
    assert.equal((await fetch(`${url}/v1/nodes/${status.nodeId}/revoke`, { method: 'POST', headers })).status, 200);
    const revoked = await command(['run']);
    assert.notEqual(revoked.code, 0);
    assert.match(revoked.stderr, /HTTP 401/);
    const helper = resolve(__dirname, '../../../scripts/enroll-local-agent.mjs');
    const helperResult = await promisify(execFile)(process.execPath, [helper, '--name', 'Helper host',
      '--state-dir', join(directory, 'helper')], {
      env: { ...process.env, MESH_ADMIN_KEY: key, PORT: String(port) }, timeout: 10_000,
    });
    assert.match(helperResult.stdout, /Enrolled Helper host/);
    assert.ok(!helperResult.stdout.includes(key));
  } finally {
    if (app) await app.close();
    else if (pool) await pool.end();
    await admin.query(`DROP SCHEMA IF EXISTS ${schema} CASCADE`);
    await admin.end();
    await rm(directory, { recursive: true, force: true });
  }
});
