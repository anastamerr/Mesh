import { z } from 'zod';
import { strict as assert } from 'node:assert';
import { test, TestContext } from 'node:test';
import { createApp } from '../src/app';
import { hashToken } from '../src/auth/tokens';
import { MemoryRepository } from './memory.repository';

const key = 'test_admin_key_012345678901234567890123456789';
const admin = { authorization: `Bearer ${key}` };
const inventory = { cpuLogicalCores: 8, memoryTotalBytes: 16_000, memoryAvailableBytes: 8_000 };

async function setup(t: TestContext) {
  const repository = new MemoryRepository();
  const app = await createApp({ adminKey: key, databaseUrl: 'postgresql://unused', host: '127.0.0.1', port: 3000 }, repository);
  t.after(() => app.close());
  await app.getHttpAdapter().getInstance().ready();
  return { repository, request: app.getHttpAdapter().getInstance().inject.bind(app.getHttpAdapter().getInstance()) };
}

test('operator routes reject missing and incorrect credentials', async t => {
  const { request } = await setup(t);
  for (const url of ['/v1/nodes', '/v1/enrollment-tokens']) {
    const method = url.endsWith('tokens') ? 'POST' : 'GET';
    assert.equal((await request({ method, url })).statusCode, 401);
    assert.equal((await request({ method, url, headers: { authorization: `Bearer ${'x'.repeat(40)}` } })).statusCode, 401);
  }
});

test('enrollment, credential isolation, monotonic heartbeat, and revocation', async t => {
  const { request, repository } = await setup(t);
  async function enroll(name: string) {
    const issued = await request({ method: 'POST', url: '/v1/enrollment-tokens', headers: admin });
    assert.equal(issued.statusCode, 201);
    assert.equal(issued.headers['cache-control'], 'no-store');
    const token = z.string().regex(/^mesh_enroll_[A-Za-z0-9_-]{43}$/).parse(issued.json().enrollmentToken);
    assert.ok(repository.tokens.has(hashToken(token)));
    assert.ok(!repository.tokens.has(token));
    const body = { enrollmentToken: token, name, platform: 'windows', architecture: 'amd64', agentVersion: '0.1.0-dev' };
    const enrolled = await request({ method: 'POST', url: '/v1/nodes/enroll', payload: body });
    assert.equal(enrolled.statusCode, 201);
    assert.equal((await request({ method: 'POST', url: '/v1/nodes/enroll', payload: body })).statusCode, 401);
    return z.object({ node: z.object({ id: z.uuid() }), nodeCredential: z.string() }).parse(enrolled.json());
  }
  const first = await enroll('Lenovo');
  const second = await enroll('Another node');
  const url = `/v1/nodes/${first.node.id}/heartbeat`;
  const headers = { authorization: `Bearer ${first.nodeCredential}` };
  assert.equal((await request({ method: 'POST', url, headers: { authorization: `Bearer ${second.nodeCredential}` },
    payload: { sequence: 0, inventory } })).statusCode, 401);
  assert.equal((await request({ method: 'POST', url, headers, payload: { sequence: 0, inventory } })).statusCode, 200);
  const seen = repository.nodes.get(first.node.id)!.lastSeenAt;
  assert.equal((await request({ method: 'POST', url, headers, payload: { sequence: 0, inventory } })).statusCode, 409);
  assert.equal(repository.nodes.get(first.node.id)!.lastSeenAt, seen);
  assert.equal((await request({ method: 'POST', url, headers, payload: { sequence: 1, inventory } })).statusCode, 200);
  const list = await request({ method: 'GET', url: '/v1/nodes', headers: admin });
  assert.equal(list.json().find((node: { id: string }) => node.id === first.node.id).status, 'online');
  assert.equal(list.json().find((node: { id: string }) => node.id === second.node.id).status, 'unknown');
  assert.ok(!list.body.includes(first.nodeCredential));
  assert.ok(!list.body.includes(hashToken(first.nodeCredential)));
  assert.equal((await request({ method: 'POST', url: `/v1/nodes/${first.node.id}/revoke`, headers: admin })).statusCode, 200);
  assert.equal((await request({ method: 'POST', url, headers, payload: { sequence: 2, inventory } })).statusCode, 401);
  const revoked = (await request({ method: 'GET', url: '/v1/nodes', headers: admin })).json();
  assert.equal(revoked.find((node: { id: string }) => node.id === first.node.id).status, 'revoked');
});

test('expired enrollment and node credentials fail; malformed input is rejected', async t => {
  const { request, repository } = await setup(t);
  const issued = (await request({ method: 'POST', url: '/v1/enrollment-tokens', headers: admin })).json();
  const payload = { enrollmentToken: issued.enrollmentToken, name: 'Lenovo', platform: 'windows', architecture: 'amd64', agentVersion: 'dev' };
  assert.equal((await request({ method: 'POST', url: '/v1/nodes/enroll', payload: { ...payload, admin: true } })).statusCode, 400);
  repository.tokens.set(hashToken(issued.enrollmentToken), new Date(0));
  assert.equal((await request({ method: 'POST', url: '/v1/nodes/enroll', payload })).statusCode, 401);
  repository.tokens.set(hashToken(issued.enrollmentToken), new Date(Date.now() + 60_000));
  const node = (await request({ method: 'POST', url: '/v1/nodes/enroll', payload })).json();
  const headers = { authorization: `Bearer ${node.nodeCredential}` };
  const url = `/v1/nodes/${node.node.id}/heartbeat`;
  assert.equal((await request({ method: 'POST', url, headers, payload: { sequence: 0,
    inventory: { ...inventory, memoryAvailableBytes: 99_000 } } })).statusCode, 400);
  assert.equal((await request({ method: 'POST', url: '/v1/nodes/not-a-uuid/heartbeat', headers,
    payload: { sequence: 0, inventory } })).statusCode, 400);
  repository.nodes.get(node.node.id)!.expires = new Date(0);
  assert.equal((await request({ method: 'POST', url, headers, payload: { sequence: 0, inventory } })).statusCode, 401);
});

test('stale observations become unreachable and readiness differs from liveness', async t => {
  const { request, repository } = await setup(t);
  const token = (await request({ method: 'POST', url: '/v1/enrollment-tokens', headers: admin })).json();
  const node = (await request({ method: 'POST', url: '/v1/nodes/enroll', payload: {
    enrollmentToken: token.enrollmentToken, name: 'Lenovo', platform: 'windows', architecture: 'amd64', agentVersion: 'dev',
  } })).json();
  repository.nodes.get(node.node.id)!.lastSeenAt = new Date(Date.now() - 61_000);
  assert.equal((await request({ method: 'GET', url: '/v1/nodes', headers: admin })).json()[0].status, 'unreachable');
  repository.available = false;
  assert.equal((await request({ method: 'GET', url: '/health/live' })).statusCode, 200);
  assert.equal((await request({ method: 'GET', url: '/health/ready' })).statusCode, 503);
});

test('storage grants isolate nodes, collections, access and expiration', async t => {
  const { request, repository } = await setup(t);
  async function enroll() {
    const issued = await request({ method: 'POST', url: '/v1/enrollment-tokens', headers: admin });
    const enrolled = await request({ method: 'POST', url: '/v1/nodes/enroll', payload: {
      enrollmentToken: issued.json().enrollmentToken, name: 'Storage host', platform: 'windows', architecture: 'amd64', agentVersion: 'dev',
    } });
    return z.object({ node: z.object({ id: z.uuid() }), nodeCredential: z.string() }).parse(enrolled.json());
  }
  const first = await enroll(), second = await enroll();
  const url = `/v1/nodes/${first.node.id}/storage-grants`;
  const permission = { access: 'write', collectionId: 'a'.repeat(64) };
  assert.equal((await request({ method: 'POST', url, payload: permission })).statusCode, 401);
  assert.equal((await request({ method: 'POST', url, headers: { authorization: `Bearer ${first.nodeCredential}` }, payload: permission })).statusCode, 401);
  for (const payload of [{ access: 'write', collectionId: null }, { access: 'list', collectionId: permission.collectionId },
    { ...permission, nodeId: second.node.id }, { access: 'delete', collectionId: permission.collectionId }]) {
    assert.equal((await request({ method: 'POST', url, headers: admin, payload })).statusCode, 400);
  }
  const issued = await request({ method: 'POST', url, headers: admin, payload: permission });
  assert.equal(issued.statusCode, 201);
  const token = z.string().regex(/^[A-Za-z0-9_-]{43}$/).parse(issued.json().token);
  assert.ok(repository.grants.has(hashToken(token)) && !repository.grants.has(token));
  async function validate(node = first, scope = permission, credential = node.nodeCredential) {
    return request({ method: 'POST', url: `/v1/nodes/${node.node.id}/storage-grants/validate`,
      headers: { authorization: `Bearer ${credential}` }, payload: { token, permission: scope } });
  }
  assert.equal((await validate()).statusCode, 200);
  assert.equal((await validate(second)).statusCode, 401);
  assert.equal((await validate(first, permission, second.nodeCredential)).statusCode, 401);
  assert.equal((await validate(first, { ...permission, access: 'read' })).statusCode, 401);
  assert.equal((await validate(first, { ...permission, collectionId: 'b'.repeat(64) })).statusCode, 401);
  repository.grants.get(hashToken(token))!.expiresAt = new Date(0);
  assert.equal((await validate()).statusCode, 401);
  repository.grants.get(hashToken(token))!.expiresAt = new Date(Date.now() + 60_000);
  repository.nodes.get(first.node.id)!.expires = new Date(0);
  assert.equal((await validate()).statusCode, 401);
  repository.nodes.get(first.node.id)!.expires = new Date(Date.now() + 60_000);
  await repository.revoke(first.node.id);
  assert.equal((await validate()).statusCode, 401);
  assert.equal((await request({ method: 'POST', url, headers: admin, payload: permission })).statusCode, 401);
});

test('catalogue distinguishes requested copies from authenticated agent confirmations', async t => {
  const { request, repository } = await setup(t);
  const issued = await request({ method: 'POST', url: '/v1/enrollment-tokens', headers: admin });
  const enrollment = await request({ method: 'POST', url: '/v1/nodes/enroll', payload: {
    enrollmentToken: issued.json().enrollmentToken, name: 'Catalogue node', platform: 'windows', architecture: 'amd64', agentVersion: 'dev',
  } });
  const node = z.object({ node: z.object({ id: z.uuid() }), nodeCredential: z.string() }).parse(enrollment.json());
  const url = `/v1/nodes/${node.node.id}/collections`;
  const entry = { id: 'a'.repeat(64), name: 'My folder', fileCount: 2, totalBytes: 1234 };
  assert.equal((await request({ method: 'POST', url, headers: admin, payload: entry })).statusCode, 201);
  const pending = await request({ method: 'GET', url, headers: admin });
  assert.equal(pending.json()[0].confirmedAt, null);
  const statistics = { id: entry.id, fileCount: entry.fileCount, totalBytes: entry.totalBytes };
  assert.equal((await request({ method: 'POST', url: `${url}/confirm`, headers: admin, payload: statistics })).statusCode, 401);
  const nodeHeaders = { authorization: `Bearer ${node.nodeCredential}` };
  assert.equal((await request({ method: 'POST', url: `${url}/confirm`, headers: nodeHeaders, payload: statistics })).statusCode, 200);
  assert.equal((await request({ method: 'POST', url, headers: admin, payload: { ...entry, name: 'Renamed' } })).statusCode, 201);
  const confirmed = (await request({ method: 'GET', url, headers: admin })).json()[0];
  assert.equal(confirmed.name, 'Renamed'); assert.ok(confirmed.confirmedAt);
  assert.equal((await request({ method: 'GET', url: `${url}?after=invalid`, headers: admin })).statusCode, 400);
  await repository.revoke(node.node.id);
  assert.equal((await request({ method: 'POST', url: `${url}/confirm`, headers: nodeHeaders, payload: statistics })).statusCode, 401);
  assert.equal((await request({ method: 'GET', url, headers: admin })).json()[0].name, 'Renamed');
});
