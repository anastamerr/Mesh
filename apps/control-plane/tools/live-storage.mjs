// Real-process experience check. Uses only an isolated schema in the test DB.
import { strict as assert } from 'node:assert';
import { execFile, spawn } from 'node:child_process';
import { createHash, randomBytes, randomUUID } from 'node:crypto';
import { once } from 'node:events';
import { createReadStream } from 'node:fs';
import { mkdir, mkdtemp, open, readFile, readdir, rm, stat, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import { Pool } from 'pg';
import { z } from 'zod';
import { createApp } from '../dist/app.js';
import { PostgresNodeRepository } from '../dist/database/postgres.repository.js';

const database = process.env.MESH_TEST_DATABASE_URL;
if (!database) throw new Error('Set MESH_TEST_DATABASE_URL to a dedicated test database.');
const binary = fileURLToPath(new URL(`../../../agent/bin/mesh-agent${process.platform === 'win32' ? '.exe' : ''}`, import.meta.url));
const helper = fileURLToPath(new URL('../../../scripts/mesh-local.mjs', import.meta.url));
const baseline = process.env.MESH_BASELINE_AGENT_BINARY;
const directory = await mkdtemp(join(tmpdir(), 'mesh-experience-'));
const schema = `mesh_live_${randomUUID().replaceAll('-', '')}`;
const admin = new Pool({ connectionString: database });
const pool = new Pool({ connectionString: database, options: `-c search_path=${schema}` });
const operatorKey = randomBytes(32).toString('base64url');
const headers = { authorization: `Bearer ${operatorKey}`, 'content-type': 'application/json' };
const children = [];
const report = { environment: `${process.platform}/${process.arch}, loopback, local disk`, measurements: [], checks: [], transcripts: {} };
let app;
let url;
let sampleTimer;
let sampling;
let maxRSSKiB = 0;
const cleanEnv = { ...process.env };
for (const name of ['MESH_ADMIN_KEY', 'DATABASE_URL', 'MESH_TEST_DATABASE_URL']) delete cleanEnv[name];
const run = (executable, args, stdin = '', env = cleanEnv) => new Promise((resolve, reject) => {
  const start = performance.now();
  const child = spawn(executable, args, { env, stdio: ['pipe', 'pipe', 'pipe'], timeout: 90_000 });
  let stdout = '', stderr = '', firstFeedbackMs;
  child.stdout.on('data', data => { stdout += data; });
  child.stderr.on('data', data => { firstFeedbackMs ??= performance.now() - start; stderr += data; });
  child.stdin.on('error', () => {});
  child.once('error', reject);
  child.once('close', code => resolve({ code, stdout, stderr, elapsedMs: performance.now() - start, firstFeedbackMs }));
  child.stdin.end(stdin);
});
const successful = result => { assert.equal(result.code, 0, result.stderr); return result; };
async function request(path, body) {
  const response = await fetch(url + path, { method: 'POST', headers, body: JSON.stringify(body) });
  assert.equal(response.status, 201);
  return response.json();
}
async function node(executable, name) {
  const state = join(directory, name, 'identity');
  const root = join(directory, name, 'files');
  const token = z.object({ enrollmentToken: z.string() }).parse(await request('/v1/enrollment-tokens', {})).enrollmentToken;
  successful(await run(executable, ['enroll', '--server', url, '--name', name, '--token-stdin', '--state-dir', state], token));
  const id = z.object({ nodeId: z.uuid() }).parse(JSON.parse(successful(await run(executable, ['status', '--state-dir', state])).stdout)).nodeId;
  const serving = executable === baseline ? ['storage', 'serve', '--enrolled'] : ['run'];
  const child = spawn(executable, [...serving, '--state-dir', state, '--root', root, '--listen', '127.0.0.1:0'], { env: cleanEnv, stdio: ['ignore', 'ignore', 'pipe'] });
  const exited = once(child, 'exit'); children.push({ child, exited });
  const endpoint = await new Promise((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error('Agent did not start')), 10_000);
    let text = '';
    child.stderr.on('data', data => { text += data; const match = /Storage listening on ([^\r\n]+)/.exec(text); if (match) { clearTimeout(timeout); resolve(`http://${match[1]}`); } });
    child.once('error', error => { clearTimeout(timeout); reject(error); });
    child.once('exit', () => { clearTimeout(timeout); reject(new Error('Agent exited before listening')); });
  });
  return { id, endpoint, child, root, state };
}
async function digest(path) {
  const hash = createHash('sha256'); for await (const chunk of createReadStream(path)) hash.update(chunk); return hash.digest('hex');
}
async function tree(path) {
  const result = {};
  async function visit(folder, prefix = '') {
    for (const entry of await readdir(folder, { withFileTypes: true })) {
      const relative = prefix + entry.name;
      if (entry.isDirectory()) { result[relative] = 'directory'; await visit(join(folder, entry.name), `${relative}/`); }
      else result[relative] = await digest(join(folder, entry.name));
    }
  }
  await visit(path); return result;
}
try {
  await admin.query(`CREATE SCHEMA ${schema}`);
  for (const file of (await readdir(fileURLToPath(new URL('../migrations', import.meta.url)))).filter(file => file.endsWith('.sql')).sort()) {
    await pool.query(await readFile(fileURLToPath(new URL(`../migrations/${file}`, import.meta.url)), 'utf8'));
  }
  app = await createApp({ databaseUrl: 'postgresql://unused', adminKey: operatorKey, host: '127.0.0.1', port: 0 }, new PostgresNodeRepository(pool));
  await app.listen(0, '127.0.0.1'); url = await app.getUrl();
  const current = await node(binary, 'Current');
  const old = baseline ? await node(baseline, 'Baseline') : undefined;
  const managed = (action, ...args) => run(process.execPath, [helper, action, '--node', 'Current', ...args], '', { ...cleanEnv, MESH_ADMIN_KEY: operatorKey, PORT: new URL(url).port });
  if (process.platform !== 'win32') sampleTimer = setInterval(() => {
    if (sampling) return;
    sampling = promisify(execFile)('/bin/ps', ['-o', 'rss=', '-p', String(current.child.pid)])
      .then(({ stdout }) => { const rss = Number(stdout.trim()); if (Number.isFinite(rss)) maxRSSKiB = Math.max(maxRSSKiB, rss); })
      .catch(() => {}).finally(() => { sampling = undefined; });
  }, 100);
  const fixtures = [ { name: 'large', files: 1, size: 128 * 1024 * 1024 }, { name: 'small-files', files: 500, size: 4096 } ];
  const block = randomBytes(1024 * 1024);
  for (const fixture of fixtures) {
    const source = join(directory, fixture.name); await mkdir(join(source, 'empty'), { recursive: true });
    for (let index = 0; index < fixture.files; index++) {
      const file = await open(join(source, `${index}.bin`), 'w', 0o600);
      try { for (let bytes = 0; bytes < fixture.size; bytes += block.length) await file.write(block.subarray(0, Math.min(block.length, fixture.size - bytes))); }
      finally { await file.close(); }
    }
    const expected = await tree(source);
    if (old) {
      const start = performance.now();
      const id = successful(await run(baseline, ['storage', 'identify', '--source', source])).stdout.trim();
      const grant = z.object({ token: z.string() }).parse(await request(`/v1/nodes/${old.id}/storage-grants`, { access: 'write', collectionId: id }));
      const key = join(directory, `${fixture.name}.key`); await writeFile(key, grant.token, { mode: 0o600 });
      successful(await run(baseline, ['storage', 'upload', '--server', old.endpoint, '--key-file', key, '--source', source]));
      report.measurements.push({ fixture: fixture.name, workflow: 'previous', elapsedMs: performance.now() - start, bytes: fixture.files * fixture.size });
    }
    const copied = successful(await managed('copy', '--server', current.endpoint, '--source', source, '--name', fixture.name, '--json'));
    const id = z.object({ id: z.string() }).parse(JSON.parse(copied.stdout)).id;
    report.measurements.push({ fixture: fixture.name, workflow: 'managed-copy', elapsedMs: copied.elapsedMs, firstFeedbackMs: copied.firstFeedbackMs, bytes: fixture.files * fixture.size });
    report.transcripts[fixture.name] = copied.stderr;
    const destination = join(directory, `${fixture.name}-download`);
    const download = successful(await managed('get', '--server', current.endpoint, '--collection', fixture.name, '--destination', destination));
    report.measurements.push({ fixture: fixture.name, workflow: 'managed-get', elapsedMs: download.elapsedMs, firstFeedbackMs: download.firstFeedbackMs, bytes: fixture.files * fixture.size });
    assert.deepEqual(await tree(destination), expected);
    assert.deepEqual(await tree(join(current.root, 'collections', id)), expected);
    const repeated = successful(await managed('copy', '--server', current.endpoint, '--source', source, '--name', fixture.name));
    assert.match(repeated.stderr, /already present/);
    report.measurements.push({ fixture: fixture.name, workflow: 'repeat-copy', elapsedMs: repeated.elapsedMs, bytes: fixture.files * fixture.size });
    console.info(`${fixture.name}: copy ${copied.elapsedMs.toFixed(0)} ms, get ${download.elapsedMs.toFixed(0)} ms; hashes match.`);
  }
  const catalogue = successful(await managed('catalog')); report.transcripts.catalogue = catalogue.stdout;
  assert.match(catalogue.stdout, /confirmed/); report.checks.push('Named catalogue shows confirmed copies');
  const missing = await managed('get', '--server', current.endpoint, '--collection', 'does-not-exist', '--destination', join(directory, 'missing'));
  assert.notEqual(missing.code, 0); assert.match(missing.stderr, /run catalog/); report.transcripts.missing = missing.stderr;
  const existing = await managed('get', '--server', current.endpoint, '--collection', 'large', '--destination', join(directory, 'large-download'));
  assert.notEqual(existing.code, 0); assert.match(existing.stderr, /destination already exists/); report.checks.push('Actionable missing-name and existing-destination errors');
  // Preserve a fixture and abort the real serving process after durable progress appears.
  const source = join(directory, 'interrupted'); await mkdir(source);
  const big = await open(join(source, 'file.bin'), 'w');
  for (let i = 0; i < 256; i++) await big.write(block);
  await big.close();
  let killed = false;
  const poll = setInterval(() => {
    // Every acknowledged upload emits progress. A separate SQLite reader is not needed:
    // a staging file beyond one chunk proves streaming has begun, then the retry verifies recovery.
    readdir(join(current.root, 'staging'), { withFileTypes: true }).then(async entries => {
      if (killed || entries.length === 0) return;
      const file = join(current.root, 'staging', entries[0].name, 'file.bin');
      const info = await stat(file);
      if (!killed && info.size > 4 * 1024 * 1024) { killed = true; current.child.kill('SIGKILL'); }
    }).catch(() => {});
  }, 5);
  let interrupted;
  try { interrupted = await managed('copy', '--server', current.endpoint, '--source', source, '--name', 'interrupted'); }
  finally { clearInterval(poll); }
  assert.ok(killed); assert.notEqual(interrupted.code, 0); assert.match(interrupted.stderr, /rerun the same command/);
  report.transcripts.interrupted = interrupted.stderr;
  const pending = successful(await managed('catalog')); assert.match(pending.stdout, /pending/);
  // Restart the same identity/root through its original CLI args; reuse the endpoint port.
  await children[0].exited;
  const restarted = spawn(binary, ['run', '--state-dir', current.state, '--root', current.root,
    '--listen', new URL(current.endpoint).host], { env: cleanEnv, stdio: ['ignore', 'ignore', 'pipe'] });
  const exited = once(restarted, 'exit'); children.push({ child: restarted, exited });
  await new Promise((resolve, reject) => { restarted.stderr.once('data', resolve); restarted.once('error', reject); });
  const resumed = successful(await managed('copy', '--server', current.endpoint, '--source', source, '--name', 'interrupted'));
  assert.match(resumed.stderr, /already present/); report.transcripts.resumed = resumed.stderr;
  successful(await managed('get', '--server', current.endpoint, '--collection', 'interrupted', '--destination', join(directory, 'resumed')));
  assert.deepEqual(await tree(source), await tree(join(directory, 'resumed')));
  report.checks.push('Real server kill: pending catalogue, resumed bytes, verified 256 MiB retrieval');
  report.result = 'PASS'; report.maxSampledServerRSSKiB = maxRSSKiB;
} catch (error) {
  report.result = 'FAIL'; report.error = error instanceof Error ? error.message : 'Live check failed'; process.exitCode = 1;
} finally {
  clearInterval(sampleTimer); await sampling;
  for (const { child, exited } of children) { if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL'); await exited; }
  if (app) await app.close(); else await pool.end();
  await admin.query(`DROP SCHEMA IF EXISTS ${schema} CASCADE`); await admin.end();
  for (const file of await readdir(directory)) await rm(join(directory, file), { recursive: true, force: true });
  await writeFile(join(directory, 'report.json'), JSON.stringify(report, null, 2) + '\n');
  console.info(`Report: ${join(directory, 'report.json')} (${report.result})`);
}
