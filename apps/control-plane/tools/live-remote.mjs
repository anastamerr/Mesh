// Actual agent/relay processes against an isolated PostgreSQL schema.
import { strict as assert } from 'node:assert';
import { spawn } from 'node:child_process';
import { createHash, randomBytes, randomUUID } from 'node:crypto';
import { mkdir, mkdtemp, open, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import { createReadStream } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';
import { Pool } from 'pg';
import { z } from 'zod';
import { createApp } from '../dist/app.js';
import { PostgresNodeRepository } from '../dist/database/postgres.repository.js';

const database = process.env.MESH_TEST_DATABASE_URL;
if (!database) throw new Error('Set MESH_TEST_DATABASE_URL to a disposable test database.');
const extension = process.platform === 'win32' ? '.exe' : '';
const binary = fileURLToPath(new URL(`../../../agent/bin/mesh-agent${extension}`, import.meta.url));
const relayBinary = fileURLToPath(new URL(`../../../agent/bin/mesh-relay${extension}`, import.meta.url));
const directory = await mkdtemp(join(tmpdir(), 'mesh-remote-'));
const schema = `mesh_remote_${randomUUID().replaceAll('-', '')}`;
const admin = new Pool({ connectionString: database });
const pool = new Pool({ connectionString: database, options: `-c search_path=${schema}` });
const operator = randomBytes(32).toString('base64url');
const relayKey = randomBytes(32).toString('base64url');
const environment = { ...process.env };
for (const key of ['DATABASE_URL', 'MESH_TEST_DATABASE_URL', 'MESH_ADMIN_KEY', 'MESH_RELAY_KEY']) delete environment[key];
const children = [];
let app;
let controller;
const report = { environment: `${process.platform}/${process.arch}; separate processes, HTTPS relay over loopback; not a WAN test`, checks: [], measurements: [] };

function start(executable, args, input = '') {
  const child = spawn(executable, args, { env: environment, stdio: ['pipe', 'pipe', 'pipe'], windowsHide: true });
  const result = { child, stdout: '', stderr: '', code: null, finished: false };
  result.done = new Promise((resolve, reject) => {
    child.once('error', reject);
    child.stdout.on('data', data => { result.stdout += data; });
    child.stderr.on('data', data => { result.stderr += data; });
    child.once('close', code => { result.code = code; result.finished = true; resolve(result); });
  });
  child.stdin.on('error', () => {});
  child.stdin.end(input);
  children.push(result);
  return result;
}
async function waitUntil(predicate, description, timeout = 15000) {
  const deadline = Date.now() + timeout;
  while (!await predicate()) {
    if (Date.now() > deadline) throw new Error(`Timed out: ${description}`);
    await delay(25);
  }
}
async function run(executable, args, input = '') {
  const process = start(executable, args, input);
  await waitUntil(() => process.finished, 'command completion', 120000);
  return process.done;
}
function success(result) { assert.equal(result.code, 0, result.stderr); return result; }
async function api(path, method = 'GET', body, expected = 200, key = operator) {
  const headers = { authorization: `Bearer ${key}`, 'content-type': 'application/json' };
  const response = method === 'GET' ? await fetch(controller + path, { headers }) : await fetch(controller + path, { method: 'POST', headers, body: JSON.stringify(body) });
  assert.equal(response.status, expected, `${method} ${path}: ${await response.clone().text()}`);
  return response.json();
}
async function digest(path) {
  const hash = createHash('sha256');
  for await (const data of createReadStream(path)) hash.update(data);
  return hash.digest('hex');
}
try {
  await admin.query(`CREATE SCHEMA ${schema}`);
  for (const file of (await readdir(fileURLToPath(new URL('../migrations', import.meta.url)))).filter(name => name.endsWith('.sql')).sort()) {
    await pool.query(await readFile(fileURLToPath(new URL(`../migrations/${file}`, import.meta.url)), 'utf8'));
  }
  const configuration = { databaseUrl: 'postgresql://unused', adminKey: operator, relayKey, relayOrigin: undefined, host: '127.0.0.1', port: 0 };
  app = await createApp(configuration, new PostgresNodeRepository(pool));
  await app.listen(0, '127.0.0.1'); controller = await app.getUrl();
  const identity = join(directory, 'identity');
  let pairing = start(binary, ['pair', '--server', controller, '--name', 'Spare laptop', '--state-dir', identity]);
  await waitUntil(() => pairing.stdout.includes('\n') || pairing.finished, 'pairing challenge');
  assert.equal(pairing.finished, false, pairing.stderr);
  const challenge = z.object({ id: z.uuid(), code: z.string() }).parse(JSON.parse(pairing.stdout.split('\n')[0]));
  const challenges = z.array(z.object({ id: z.uuid(), code: z.string(), publicKeyFingerprint: z.string() })).parse(await api('/v1/pairing-challenges'));
  const listed = challenges.find(item => item.id === challenge.id);
  assert.ok(listed); assert.equal(listed.code, challenge.code);
  await api(`/v1/pairing-challenges/${challenge.id}/approve`, 'POST', { publicKeyFingerprint: '0'.repeat(64) }, 409);
  // Kill the requester before approval: restart must reuse its protected secret.
  pairing.child.kill('SIGKILL'); await pairing.done;
  success(await run(binary, ['pair', 'list', '--server', controller, '--operator-stdin'], operator));
  success(await run(binary, ['pair', 'approve', '--server', controller, '--operator-stdin', '--code', listed.code,
    '--fingerprint', listed.publicKeyFingerprint], operator));
  const approved = z.object({ node: z.object({ id: z.uuid() }) }).parse(await api(`/v1/pairing-challenges/${challenge.id}/approve`, 'POST', { publicKeyFingerprint: listed.publicKeyFingerprint }));
  const repeated = z.object({ node: z.object({ id: z.uuid() }) }).parse(await api(`/v1/pairing-challenges/${challenge.id}/approve`, 'POST', { publicKeyFingerprint: listed.publicKeyFingerprint }));
  assert.equal(repeated.node.id, approved.node.id);
  pairing = await run(binary, ['pair', '--server', controller, '--name', 'Spare laptop', '--state-dir', identity]); success(pairing);
  await pool.query("UPDATE nodes SET credential_expires_at=now()+interval '1 day' WHERE id=$1", [approved.node.id]);
  const renewed = z.object({ credentialExpiresAt: z.string() }).parse(JSON.parse(success(await run(binary, ['renew', '--state-dir', identity])).stdout));
  assert.ok(Date.parse(renewed.credentialExpiresAt) > Date.now() + 27 * 86400000);
  const renewedAgain = z.object({ credentialExpiresAt: z.string() }).parse(JSON.parse(success(await run(binary, ['renew', '--state-dir', identity])).stdout));
  assert.equal(renewedAgain.credentialExpiresAt, renewed.credentialExpiresAt);
  report.checks.push('Paired device lease renewal is authenticated and repeatable');
  report.checks.push('Pairing survives requester death and repeated approval creates one identity; mismatched fingerprint denied');
  console.info('Pairing and restart recovery passed.');
  success(await run(process.env.MESH_GO_BINARY ?? 'go', ['run', fileURLToPath(new URL('./remote-cert.go', import.meta.url)), directory]));
  const cert = join(directory, 'relay-cert.pem');
  const key = join(directory, 'relay-key.pem');
  const service = join(directory, 'relay-service'); await writeFile(service, relayKey, { mode: 0o600 });
  await api('/v1/nodes', 'GET', undefined, 401, relayKey);
  report.checks.push('Relay service credential cannot administer devices');
  const relayArgs = ['--listen', '127.0.0.1:0', '--tls-cert', cert, '--tls-key', key, '--auth-url', `${controller}/v1/relay/authorize`, '--auth-service-token-file', service];
  let relay = start(relayBinary, relayArgs);
  await waitUntil(() => relay.stderr.includes('Relay listening on ') || relay.finished, 'relay startup');
  assert.equal(relay.finished, false, relay.stderr);
  const address = /Relay listening on ([^\r\n]+)/.exec(relay.stderr)?.[1]; assert.ok(address);
  const origin = `https://${address}`;
  configuration.relayOrigin = origin;
  const storageRoot = join(directory, 'storage');
  const device = start(binary, ['run', '--state-dir', identity, '--root', storageRoot, '--listen', '127.0.0.1:0', '--relay-ca', cert, '--interval', '1s']);
  await waitUntil(() => device.stderr.includes('Storage listening on ') || device.finished, 'storage startup');
  assert.equal(device.finished, false, device.stderr);
  console.info('Agent and HTTPS relay running.');
  const managed = (action, ...args) => run(binary, ['storage', action, '--controller', controller, '--node', approved.node.id, '--operator-stdin', '--relay-ca', cert, ...args], operator);
  const source = join(directory, 'source'); await mkdir(source);
  const block = randomBytes(1024 * 1024);
  const large = await open(join(source, 'large.bin'), 'w', 0o600);
  try { for (let i = 0; i < 128; i++) await large.write(block); } finally { await large.close(); }
  const expected = await digest(join(source, 'large.bin'));
  const began = performance.now();
  const copied = success(await managed('copy', '--source', source, '--name', 'Remote copy', '--json'));
  console.info('128 MiB relayed upload completed.');
  report.measurements.push({ operation: 'relay upload', bytes: 128 * 1024 * 1024, elapsedMs: performance.now() - began });
  const collection = z.object({ id: z.string() }).parse(JSON.parse(copied.stdout)).id;
  const grant = z.object({ token: z.string() }).parse(await api(`/v1/nodes/${approved.node.id}/storage-grants`, 'POST', { access: 'read', collectionId: collection }, 201));
  const ticket = z.object({ token: z.string() }).parse(await api(`/v1/nodes/${approved.node.id}/relay-tickets`, 'POST', { role: 'consumer' }, 201, grant.token));
  await api('/v1/relay/authorize', 'POST', { role: 'consumer', nodeId: approved.node.id, token: ticket.token }, 200, relayKey);
  await api('/v1/relay/authorize', 'POST', { role: 'consumer', nodeId: approved.node.id, token: grant.token }, 401, relayKey);
  await api('/v1/relay/authorize', 'POST', { role: 'device', nodeId: approved.node.id, token: ticket.token }, 401, relayKey);
  await api(`/v1/nodes/${approved.node.id}/renew`, 'POST', {}, 401, ticket.token);
  await api(`/v1/nodes/${approved.node.id}/relay-tickets`, 'POST', { role: 'consumer' }, 401, ticket.token);
  report.checks.push('Relay tickets cannot renew identity, mint tickets, or change role; storage grants cannot authenticate to relay');
  assert.equal(await digest(join(storageRoot, 'collections', collection, 'large.bin')), expected);
  const destination = join(directory, 'download');
  const args = ['storage', 'get', '--controller', controller, '--node', approved.node.id, '--operator-stdin', '--relay-ca', cert, '--collection', collection, '--destination', destination];
  const getting = start(binary, args, operator);
  const journal = join(directory, '.download.mesh-download');
  await waitUntil(async () => {
    if (getting.finished) throw new Error(`Download ended before interruption: ${getting.stderr}`);
    try { return (await readFile(journal, 'utf8')).split('\n').length > 3; } catch { return false; }
  }, 'durable download checkpoint');
  getting.child.kill('SIGKILL'); await getting.done;
  const checkpointText = await readFile(journal, 'utf8');
  // A killed process can leave an incomplete final journal line.
  const checkpoints = checkpointText.slice(0, checkpointText.lastIndexOf('\n')).split('\n').slice(1)
    .map(line => z.object({ offset: z.number() }).parse(JSON.parse(line)));
  const savedBytes = checkpoints.at(-1)?.offset ?? 0; assert.ok(savedBytes > 0);
  // Restart the relay at the same endpoint. The serving laptop must reconnect.
  relay.child.kill('SIGKILL'); await relay.done;
  relay = start(relayBinary, ['--listen', address, ...relayArgs.slice(2)]);
  await waitUntil(() => relay.stderr.includes('Relay listening on '), 'relay restart');
  await delay(1800);
  const resumedAt = performance.now();
  const resumed = success(await run(binary, args, operator));
  assert.match(resumed.stderr, /already present/);
  assert.equal(await digest(join(destination, 'large.bin')), expected);
  report.measurements.push({ operation: 'resumed relay download', savedBytes, bytes: 128 * 1024 * 1024, elapsedMs: performance.now() - resumedAt });
  report.checks.push('128 MiB relay upload/retrieval hashes match; killed downloader resumes durable bytes after relay restart');
  // A paired-key mismatch must fail before any downloaded output is created.
  await pool.query('UPDATE nodes SET public_key_fingerprint=$2 WHERE id=$1', [approved.node.id, '0'.repeat(64)]);
  const mismatched = await managed('get', '--collection', collection, '--destination', join(directory, 'wrong-device'));
  assert.notEqual(mismatched.code, 0);
  assert.equal((await readdir(directory)).includes('wrong-device'), false);
  await pool.query('UPDATE nodes SET public_key_fingerprint=$2 WHERE id=$1', [approved.node.id, listed.publicKeyFingerprint]);
  report.checks.push('Wrong paired TLS key is rejected before creating output');
  const corruptDestination = join(directory, 'corrupt-copy');
  const corruptArgs = [...args.slice(0, -1), corruptDestination];
  const corrupting = start(binary, corruptArgs, operator);
  const corruptJournal = join(directory, '.corrupt-copy.mesh-download');
  await waitUntil(async () => {
    if (corrupting.finished) throw new Error(`Download ended before corruption check: ${corrupting.stderr}`);
    try { return (await readFile(corruptJournal, 'utf8')).split('\n').length > 3; } catch { return false; }
  }, 'checkpoint before local corruption');
  corrupting.child.kill('SIGKILL'); await corrupting.done;
  const saved = z.object({ staging: z.string().regex(/^\.mesh-download-[a-f0-9]{24}$/) }).parse(JSON.parse((await readFile(corruptJournal, 'utf8')).split('\n')[0]));
  const partial = await open(join(directory, saved.staging, 'large.bin'), 'r+');
  try { await partial.write(Buffer.from('corrupt'), 0, 7, 0); await partial.sync(); } finally { await partial.close(); }
  const repaired = success(await run(binary, corruptArgs, operator));
  assert.doesNotMatch(repaired.stderr, /already present/);
  assert.equal(await digest(join(corruptDestination, 'large.bin')), expected);
  report.checks.push('Corrupt saved prefix is discarded and downloaded again with matching final hash');
  const occupied = join(directory, 'occupied'); await mkdir(occupied);
  await writeFile(join(occupied, 'user.txt'), 'keep this file');
  const conflict = await managed('get', '--collection', collection, '--destination', occupied);
  assert.notEqual(conflict.code, 0);
  assert.equal(await readFile(join(occupied, 'user.txt'), 'utf8'), 'keep this file');
  report.checks.push('Existing user destination remains untouched');
  const small = join(directory, 'small'); await mkdir(small);
  for (let i = 0; i < 200; i++) await writeFile(join(small, `${i}.txt`), block.subarray(0, 4096));
  success(await managed('copy', '--source', small, '--name', 'Small files'));
  success(await managed('get', '--collection', 'Small files', '--destination', join(directory, 'small-copy')));
  assert.equal((await readdir(join(directory, 'small-copy'))).length, 200);
  for (const file of await readdir(small)) assert.equal(await digest(join(small, file)), await digest(join(directory, 'small-copy', file)));
  report.checks.push('200 small files copied and retrieved through relay with all hashes verified');
  await api(`/v1/nodes/${approved.node.id}/revoke`, 'POST', {});
  await api(`/v1/pairing-challenges/${challenge.id}/approve`, 'POST', { publicKeyFingerprint: listed.publicKeyFingerprint }, 410);
  const denied = await managed('get', '--collection', collection, '--destination', join(directory, 'denied'));
  assert.notEqual(denied.code, 0);
  report.checks.push('Revoked device cannot obtain fresh remote access');
  report.result = 'PASS';
  await writeFile(join(directory, 'report.json'), JSON.stringify(report, null, 2));
  console.info(JSON.stringify(report, null, 2));
  console.info(`Report: ${join(directory, 'report.json')}`);
} finally {
  for (const entry of children) { if (!entry.finished) entry.child.kill('SIGKILL'); await entry.done.catch(() => {}); }
  if (app) await app.close(); else await pool.end();
  await admin.query(`DROP SCHEMA IF EXISTS ${schema} CASCADE`); await admin.end();
  // Keep report and fixtures for diagnosis; remove only the known credential files.
  for (const name of ['relay-service', 'relay-key.pem']) await rm(join(directory, name), { force: true });
}
