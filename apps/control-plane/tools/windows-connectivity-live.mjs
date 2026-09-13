// Native Windows direct-LAN validation. The SSH identity and a current Windows
// Mesh binary must already exist on the target. All remote and database state
// created by this runner is isolated and removed in finally.
import { strict as assert } from 'node:assert';
import { spawn } from 'node:child_process';
import { createHash, randomBytes, randomUUID } from 'node:crypto';
import { createReadStream } from 'node:fs';
import { mkdir, mkdtemp, open, readFile, readdir, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';
import { Pool } from 'pg';
import { z } from 'zod';
import { createApp } from '../dist/app.js';
import { PostgresNodeRepository } from '../dist/database/postgres.repository.js';
import { PostgresWorkloadRepository } from '../dist/database/postgres.workload-repository.js';

const configuration = z.object({
  MESH_TEST_DATABASE_URL: z.string().min(1),
  MESH_WINDOWS_HOST: z.string().regex(/^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+$/),
  MESH_WINDOWS_KEY: z.string().min(1),
  MESH_WINDOWS_KNOWN_HOSTS: z.string().min(1),
  MESH_WINDOWS_BINARY: z.string().regex(/^[A-Za-z]:[\\/][^'"\r\n]+\.exe$/i),
  MESH_WINDOWS_CLIENT_BINARY: z.string().min(1).optional(),
}).parse(process.env);

const clientBinary = configuration.MESH_WINDOWS_CLIENT_BINARY
  ?? fileURLToPath(new URL('../../../agent/bin/mesh-agent', import.meta.url));
const migrations = fileURLToPath(new URL('../migrations', import.meta.url));
const evidence = await mkdtemp(join(tmpdir(), 'mesh-windows-connectivity-'));
const remoteDirectory = `F:/Mesh-Connectivity-${randomUUID().replaceAll('-', '')}`;
const remoteExecutable = `${remoteDirectory}/mesh-agent.exe`;
const remoteState = `${remoteDirectory}/identity`;
const remoteRoot = `${remoteDirectory}/storage`;
const firewallRule = `Mesh Direct Live ${randomUUID()}`;
const schema = `mesh_connectivity_${randomUUID().replaceAll('-', '')}`;
const operator = randomBytes(32).toString('base64url');
const admin = new Pool({ connectionString: configuration.MESH_TEST_DATABASE_URL });
const pool = new Pool({ connectionString: configuration.MESH_TEST_DATABASE_URL, options: `-c search_path=${schema}` });
const sshArguments = ['-o', 'IPQoS=none', '-i', configuration.MESH_WINDOWS_KEY, '-o', 'IdentitiesOnly=yes',
  '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=8', '-o', 'ServerAliveInterval=10', '-o', 'ServerAliveCountMax=3',
  '-o', 'StrictHostKeyChecking=yes', '-o', `UserKnownHostsFile=${configuration.MESH_WINDOWS_KNOWN_HOSTS}`];
const children = new Set();
const report = { date: new Date().toISOString(), environment: 'macOS client/controller to native Windows direct private-LAN storage',
  result: 'RUNNING', checks: [], measurements: [], remoteDirectory };
let app;
let controller;
let remoteCreated = false;
let schemaCreated = false;
let firewallAdded = false;

function start(command, args, input = '') {
  const child = spawn(command, args, { stdio: ['pipe', 'pipe', 'pipe'] });
  children.add(child);
  let stdout = '';
  let stderr = '';
  const done = new Promise((resolve, reject) => {
    child.once('error', reject);
    child.stdout.on('data', data => { stdout += data; });
    child.stderr.on('data', data => { stderr += data; });
    child.once('close', (code, signal) => { children.delete(child); resolve({ code, signal, stdout, stderr }); });
  });
  child.stdin.on('error', () => {});
  child.stdin.end(input);
  return { child, done, stdout: () => stdout, stderr: () => stderr };
}

async function run(command, args, input = '', timeout = 5 * 60_000) {
  const process = start(command, args, input);
  const timer = setTimeout(() => process.child.kill('SIGKILL'), timeout);
  try { return await process.done; } finally { clearTimeout(timer); }
}

function successful(result, label) {
  assert.equal(result.code, 0, `${label}: ${result.stderr || result.stdout}`);
  return result;
}

async function waitUntil(predicate, label, timeout = 20_000) {
  const deadline = Date.now()+timeout;
  while (!await predicate()) {
    if (Date.now() >= deadline) throw new Error(`Timed out: ${label}`);
    await delay(50);
  }
}

const encodedPowerShell = script => ['powershell', '-NoProfile', '-NonInteractive', '-EncodedCommand',
  Buffer.from(`$ProgressPreference='SilentlyContinue'; $ErrorActionPreference='Stop';\n${script}`, 'utf16le').toString('base64')].join(' ');
const ssh = (script, input = '', timeout) => run('ssh', [...sshArguments, configuration.MESH_WINDOWS_HOST, script], input, timeout);
const powerShell = (script, timeout) => ssh(encodedPowerShell(script), '', timeout);

async function api(path, options = {}) {
  const response = await fetch(controller+path, { method: options.method ?? 'GET', headers: {
    authorization: `Bearer ${options.key ?? operator}`, 'content-type': 'application/json',
  }, body: options.body === undefined ? undefined : JSON.stringify(options.body), signal: AbortSignal.timeout(15_000) });
  assert.equal(response.status, options.status ?? 200, `${path}: ${await response.clone().text()}`);
  return response.json();
}

async function digest(path) {
  const hash = createHash('sha256');
  for await (const data of createReadStream(path)) hash.update(data);
  return hash.digest('hex');
}

try {
  await admin.query(`CREATE SCHEMA ${schema}`);
  schemaCreated = true;
  for (const name of (await readdir(migrations)).filter(name => name.endsWith('.sql')).sort()) {
    await pool.query(await readFile(join(migrations, name), 'utf8'));
  }
  app = await createApp({ databaseUrl: 'postgresql://unused', adminKey: operator, host: '127.0.0.1', port: 0 },
    { nodes: new PostgresNodeRepository(pool), workloads: new PostgresWorkloadRepository(pool) });
  await app.listen(0, '127.0.0.1');
  controller = await app.getUrl();

  remoteCreated = true;
  successful(await powerShell(`New-Item -ItemType Directory -Path '${remoteDirectory}' | Out-Null; $identity=$env:USERNAME + ':(OI)(CI)F'; icacls.exe '${remoteDirectory}' /inheritance:r /grant $identity '*S-1-5-18:(OI)(CI)F'; if ($LASTEXITCODE -ne 0) { throw 'ACL setup failed' }; Copy-Item -LiteralPath '${configuration.MESH_WINDOWS_BINARY}' -Destination '${remoteExecutable}'`, 120_000), 'remote setup');
  successful(await powerShell(`netsh advfirewall firewall add rule name='${firewallRule}' dir=in action=allow protocol=TCP localport=17332 profile=private | Out-Null; if ($LASTEXITCODE -ne 0) { throw 'Firewall rule failed' }`), 'temporary firewall rule');
  firewallAdded = true;
  const reverse = start('ssh', [...sshArguments, '-o', 'ExitOnForwardFailure=yes', '-N', '-R',
    `127.0.0.1:17300:127.0.0.1:${new URL(controller).port}`, configuration.MESH_WINDOWS_HOST]);
  await waitUntil(async () => (await powerShell("try { (Invoke-WebRequest -UseBasicParsing 'http://127.0.0.1:17300/health/ready' -TimeoutSec 2).StatusCode } catch { 0 }")).stdout.trim() === '200', 'controller reverse tunnel');

  const pairing = start('ssh', [...sshArguments, configuration.MESH_WINDOWS_HOST,
    `${remoteExecutable} pair --server http://127.0.0.1:17300 --name "IdeaPad Direct Live" --state-dir ${remoteState}`]);
  await waitUntil(() => pairing.stdout().includes('\n'), 'pairing challenge');
  const challenge = z.object({ id: z.uuid() }).parse(JSON.parse(pairing.stdout().split(/\r?\n/)[0]));
  const pending = z.array(z.object({ id: z.uuid(), publicKeyFingerprint: z.string() })).parse(await api('/v1/pairing-challenges'));
  const selected = pending.find(item => item.id === challenge.id);
  assert.ok(selected);
  await api(`/v1/pairing-challenges/${challenge.id}/approve`, { method: 'POST', body: { publicKeyFingerprint: selected.publicKeyFingerprint } });
  successful(await pairing.done, 'pairing completion');

  const node = z.object({ id: z.uuid(), name: z.string() }).parse((await api('/v1/nodes')).find(item => item.name === 'IdeaPad Direct Live'));
  const serving = start('ssh', [...sshArguments, configuration.MESH_WINDOWS_HOST,
    `${remoteExecutable} run --state-dir ${remoteState} --root ${remoteRoot} --direct-lan --listen 0.0.0.0:17332 --interval 1s`]);
  await waitUntil(() => serving.stderr().includes('Storage listening') || serving.child.exitCode !== null, 'Windows direct storage');
  assert.equal(serving.child.exitCode, null, serving.stderr());
  const connectionSchema = z.object({ directCandidates: z.array(z.object({ transport: z.literal('tcp'), host: z.string(), port: z.number() })) });
  let candidates;
  await waitUntil(async () => {
    candidates = connectionSchema.parse(await api(`/v1/nodes/${node.id}/connection`)).directCandidates;
    return candidates.some(candidate => candidate.port === 17332);
  }, 'fresh Windows candidates');
  report.candidates = candidates;

  const source = join(evidence, 'source');
  await mkdir(source);
  const content = randomBytes(8*1024*1024);
  const file = await open(join(source, 'direct.bin'), 'w', 0o600);
  try { await file.write(content); await file.sync(); } finally { await file.close(); }
  const expected = await digest(join(source, 'direct.bin'));
  const managed = (action, args) => run(clientBinary, ['storage', action, '--controller', controller, '--node', node.id,
    '--operator-stdin', ...args], operator, 5*60_000);
  const copiedAt = performance.now();
  const copied = successful(await managed('copy', ['--source', source, '--name', 'Windows direct', '--json']), 'direct copy');
  assert.match(copied.stderr, /Connected directly/);
  report.measurements.push({ operation: 'direct upload', bytes: content.length, elapsedMs: performance.now()-copiedAt });
  const collection = z.object({ id: z.string().regex(/^[a-f0-9]{64}$/) }).parse(JSON.parse(copied.stdout));
  const destination = join(evidence, 'destination');
  const retrievedAt = performance.now();
  const retrieved = successful(await managed('get', ['--collection', collection.id, '--destination', destination]), 'direct get');
  assert.match(retrieved.stderr, /Connected directly/);
  assert.equal(await digest(join(destination, 'direct.bin')), expected);
  report.measurements.push({ operation: 'direct download', bytes: content.length, elapsedMs: performance.now()-retrievedAt });
  report.checks.push('Native Windows agent published fresh private-LAN candidates through the controller');
  report.checks.push('Mac client selected the paired TLS-authenticated direct path without a relay');
  report.checks.push('8 MiB upload, Windows publication, retrieval and SHA-256 verification completed');
  await api(`/v1/nodes/${node.id}/revoke`, { method: 'POST', body: {} });
  report.result = 'PASS';
  await writeFile(join(evidence, 'report.json'), `${JSON.stringify(report, null, 2)}\n`, { mode: 0o600 });
  console.info(`Report: ${join(evidence, 'report.json')} (PASS)`);
  reverse.child.kill('SIGTERM');
} catch (error) {
  report.result = 'FAIL';
  report.error = error instanceof Error ? error.message : 'Unknown failure';
  await writeFile(join(evidence, 'report.json'), `${JSON.stringify(report, null, 2)}\n`, { mode: 0o600 });
  throw error;
} finally {
  for (const child of children) child.kill('SIGKILL');
  if (firewallAdded) {
    await powerShell(`netsh advfirewall firewall delete rule name='${firewallRule}' | Out-Null`, 30_000).catch(() => {});
  }
  if (remoteCreated) {
    const windowsPath = remoteExecutable.replaceAll('/', '\\');
    await powerShell(`Get-Process -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq '${windowsPath}' } | Stop-Process -Force; Remove-Item -LiteralPath '${remoteDirectory}' -Recurse -Force`, 60_000).catch(() => {});
  }
  if (app) await app.close(); else await pool.end();
  if (schemaCreated) await admin.query(`DROP SCHEMA IF EXISTS ${schema} CASCADE`);
  await admin.end();
}
