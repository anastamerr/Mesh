// Native Windows storage experience check. The remote host and binary must already exist.
import { strict as assert } from 'node:assert';
import { spawn } from 'node:child_process';
import { createHash, randomBytes, randomUUID } from 'node:crypto';
import { createReadStream } from 'node:fs';
import { mkdir, mkdtemp, open, readFile, readdir, rm, rename, writeFile } from 'node:fs/promises';
import { createServer, connect } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { Pool } from 'pg';
import { z } from 'zod';
import { createApp } from '../dist/app.js';
import { PostgresNodeRepository } from '../dist/database/postgres.repository.js';

const configurationSchema = z.object({
  MESH_TEST_DATABASE_URL: z.string().min(1),
  MESH_WINDOWS_HOST: z.string().regex(/^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+$/),
  MESH_WINDOWS_KEY: z.string().min(1),
  MESH_WINDOWS_KNOWN_HOSTS: z.string().min(1),
  MESH_WINDOWS_BINARY: z.string().regex(/^[A-Za-z]:[\\/][^'"\r\n]+\.exe$/i),
  MESH_WINDOWS_CLIENT_BINARY: z.string().min(1).optional(),
  MESH_WINDOWS_RECOVERY: z.enum(['0', '1']).default('1'),
});
const parsedConfiguration = configurationSchema.safeParse(process.env);
if (!parsedConfiguration.success) {
  throw new Error(`Invalid Windows live-test environment: ${z.prettifyError(parsedConfiguration.error)}`);
}
const configuration = parsedConfiguration.data;
const defaultClient = fileURLToPath(new URL('../../../agent/bin/mesh-agent', import.meta.url));
const clientBinary = configuration.MESH_WINDOWS_CLIENT_BINARY ?? defaultClient;
const migrations = fileURLToPath(new URL('../migrations', import.meta.url));
const evidenceDirectory = await mkdtemp(join(tmpdir(), 'mesh-windows-live-'));
const workDirectory = join(evidenceDirectory, 'work');
await mkdir(workDirectory);
const reportPath = join(evidenceDirectory, 'report.json');
const suffix = randomUUID().replaceAll('-', '');
const schema = `mesh_windows_${suffix}`;
const remoteDirectory = `F:/Mesh-Live-${suffix}`;
const remoteExecutable = `${remoteDirectory}/mesh-agent.exe`;
const remoteState = `${remoteDirectory}/identity`;
const remoteRoot = `${remoteDirectory}/files`;
const operatorKey = randomBytes(32).toString('base64url');
const cleanEnvironment = { ...process.env };
for (const key of ['DATABASE_URL', 'MESH_TEST_DATABASE_URL', 'MESH_ADMIN_KEY']) delete cleanEnvironment[key];
const sshArguments = ['-o', 'IPQoS=none', '-i', configuration.MESH_WINDOWS_KEY, '-o', 'IdentitiesOnly=yes',
  '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=8', '-o', 'ServerAliveInterval=10', '-o', 'ServerAliveCountMax=3',
  '-o', 'StrictHostKeyChecking=yes', '-o', `UserKnownHostsFile=${configuration.MESH_WINDOWS_KNOWN_HOSTS}`];
const report = {
  date: new Date().toISOString(),
  environment: 'macOS controller/client to native Windows agent; separate SSH control and data tunnels',
  clientBinary,
  recoveryEnabled: configuration.MESH_WINDOWS_RECOVERY === '1',
  remoteDirectory,
  result: 'RUNNING',
  stages: [],
  measurements: [],
  checks: [],
  transcripts: {},
  limitations: [
    'Single iterations are experience checks, not statistical throughput benchmarks.',
    'SSH, Wi-Fi, disk, hashing, durability, and Mesh overhead are not independently isolated by this runner.',
    'Copy timings include local scanning, controller registration and grant issuance; get timings include catalogue lookup.',
    'First log timing is user-visible feedback, not time to first transferred byte.',
  ],
};
const children = new Set();
const admin = new Pool({ connectionString: configuration.MESH_TEST_DATABASE_URL });
const pool = new Pool({ connectionString: configuration.MESH_TEST_DATABASE_URL, options: `-c search_path=${schema}` });
let app;
let controllerUrl;
let remoteCreated = false;
let schemaCreated = false;
let storagePID;

async function saveReport() {
  const temporary = `${reportPath}.tmp`;
  await writeFile(temporary, `${JSON.stringify(report, null, 2)}\n`, { mode: 0o600 });
  await rename(temporary, reportPath);
}

function startProcess(command, args, options = {}) {
  const started = performance.now();
  const child = spawn(command, args, { env: options.env ?? cleanEnvironment, stdio: ['pipe', 'pipe', 'pipe'] });
  children.add(child);
  let stdout = '';
  let stderr = '';
  let firstLogMs;
  let settled = false;
  const timeoutMs = options.timeoutMs ?? 90_000;
  const timeout = setTimeout(() => {
    child.kill('SIGTERM');
    setTimeout(() => { if (!settled) child.kill('SIGKILL'); }, 2_000).unref();
  }, timeoutMs);
  const done = new Promise((resolve, reject) => {
    child.once('error', error => { clearTimeout(timeout); settled = true; children.delete(child); reject(error); });
    child.stdout.on('data', data => { stdout += data; options.onStdout?.(data.toString()); });
    child.stderr.on('data', data => {
      firstLogMs ??= performance.now() - started;
      stderr += data;
      options.onStderr?.(data.toString());
    });
    child.once('close', (code, signal) => {
      clearTimeout(timeout); settled = true; children.delete(child);
      resolve({ code, signal, stdout, stderr, elapsedMs: performance.now() - started, firstLogMs,
        timedOut: performance.now() - started >= timeoutMs });
    });
  });
  child.stdin.on('error', () => {});
  child.stdin.end(options.stdin ?? '');
  return { child, done };
}

async function run(command, args, options) {
  return startProcess(command, args, options).done;
}

function successful(result, context) {
  assert.equal(result.timedOut, false, `${context} timed out`);
  assert.equal(result.code, 0, `${context}: ${result.stderr || result.stdout}`);
  return result;
}

const powerShellCommand = script => ['powershell', '-NoProfile', '-NonInteractive', '-EncodedCommand',
  Buffer.from(`$ProgressPreference='SilentlyContinue'; $ErrorActionPreference='Stop';\n${script}`, 'utf16le').toString('base64')].join(' ');
const ssh = (script, options = {}) => run('ssh', [...sshArguments, configuration.MESH_WINDOWS_HOST, script], options);
const powerShell = (script, options = {}) => ssh(powerShellCommand(script), options);

async function freePort() {
  const server = createServer();
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve); });
  const address = z.object({ port: z.number().int().positive() }).parse(server.address());
  await new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve()));
  return address.port;
}

async function waitForLocalPort(port, processHandle, label) {
  const deadline = performance.now() + 15_000;
  while (performance.now() < deadline) {
    if (processHandle.child.exitCode !== null || processHandle.child.signalCode !== null) {
      const result = await processHandle.done;
      throw new Error(`${label} exited before readiness: ${result.stderr}`);
    }
    const connected = await new Promise(resolve => {
      const socket = connect({ host: '127.0.0.1', port });
      socket.setTimeout(500);
      socket.once('connect', () => { socket.destroy(); resolve(true); });
      socket.once('timeout', () => { socket.destroy(); resolve(false); });
      socket.once('error', () => resolve(false));
    });
    if (connected) return;
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  throw new Error(`${label} did not become ready`);
}

async function stage(name, action) {
  const started = performance.now();
  try {
    const value = await action();
    report.stages.push({ name, result: 'PASS', elapsedMs: performance.now() - started });
    await saveReport();
    return value;
  } catch (error) {
    report.stages.push({ name, result: 'FAIL', elapsedMs: performance.now() - started,
      error: error instanceof Error ? error.message : 'Unknown failure' });
    await saveReport();
    throw error;
  }
}

async function controllerRequest(path, body) {
  assert.ok(controllerUrl);
  const response = await fetch(controllerUrl + path, { method: 'POST', headers: {
    authorization: `Bearer ${operatorKey}`, 'content-type': 'application/json',
  }, body: JSON.stringify(body), signal: AbortSignal.timeout(15_000) });
  assert.ok(response.ok, `Controller returned HTTP ${response.status}`);
  return response.json();
}

async function digest(path) {
  const hash = createHash('sha256');
  for await (const chunk of createReadStream(path)) hash.update(chunk);
  return hash.digest('hex');
}

async function tree(path) {
  const result = {};
  async function visit(directory, prefix = '') {
    for (const entry of await readdir(directory, { withFileTypes: true })) {
      const relative = prefix + entry.name;
      if (entry.isDirectory()) {
        result[relative] = 'directory';
        await visit(join(directory, entry.name), `${relative}/`);
      } else {
        result[relative] = await digest(join(directory, entry.name));
      }
    }
  }
  await visit(path);
  return result;
}

const fixtureBlock = randomBytes(1024 * 1024);
async function createFixture(name, count, size) {
  const source = join(workDirectory, name);
  await mkdir(join(source, 'empty'), { recursive: true });
  for (let index = 0; index < count; index++) {
    const file = await open(join(source, `${index}.bin`), 'w', 0o600);
    try {
      for (let written = 0; written < size; written += fixtureBlock.length) {
        await file.write(fixtureBlock.subarray(0, Math.min(fixtureBlock.length, size - written)));
      }
    } finally {
      await file.close();
    }
  }
  return source;
}

function managed(action, args = [], options = {}) {
  assert.ok(controllerUrl);
  const storageArguments = ['storage', action, '--controller', controllerUrl, '--operator-stdin', '--node', 'IdeaPad Windows Live'];
  if (action !== 'catalog') storageArguments.push('--server', `http://127.0.0.1:${dataPort}`);
  storageArguments.push(...args);
  return run(clientBinary, storageArguments, {
    ...options,
    stdin: operatorKey,
    timeoutMs: options.timeoutMs ?? 10 * 60_000,
  });
}

async function stopStorage() {
  if (!remoteCreated && !storagePID) return;
  const windowsPath = remoteExecutable.replaceAll('/', '\\');
  const pidFilter = storagePID ? ` | Where-Object { $_.Id -eq ${storagePID} }` : '';
  successful(await powerShell(`$processes=@(Get-Process -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq '${windowsPath}' }${pidFilter}); foreach ($process in $processes) { Stop-Process -Id $process.Id -Force }`, { timeoutMs: 30_000 }), 'stop storage');
  storagePID = undefined;
}

async function startStorage() {
  let logs = '';
  const serving = startProcess('ssh', [...sshArguments, configuration.MESH_WINDOWS_HOST,
    `${remoteExecutable} storage serve --enrolled --state-dir ${remoteState} --root ${remoteRoot} --listen 127.0.0.1:17332`], {
    timeoutMs: 30 * 60_000,
    onStderr: text => { logs += text; },
  });
  const deadline = performance.now() + 15_000;
  while (!logs.includes('Storage listening') && performance.now() < deadline) {
    if (serving.child.exitCode !== null || serving.child.signalCode !== null) break;
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  assert.match(logs, /Storage listening/, `Storage did not start: ${logs}`);
  const result = successful(await powerShell('(Get-NetTCPConnection -LocalPort 17332 -State Listen).OwningProcess', { timeoutMs: 30_000 }), 'find storage PID');
  storagePID = z.coerce.number().int().positive().parse(result.stdout.trim());
  await waitForLocalPort(dataPort, dataTunnel, 'data tunnel');
  const probe = await fetch(`http://127.0.0.1:${dataPort}/v1/collections`, { signal: AbortSignal.timeout(5_000) });
  assert.equal(probe.status, 401, 'Data tunnel did not reach the storage agent');
  return serving;
}

const dataPort = await freePort();
let dataTunnel;
let servingProcess;

try {
  await saveReport();
  await stage('database and controller', async () => {
    await admin.query(`CREATE SCHEMA ${schema}`);
    schemaCreated = true;
    for (const file of (await readdir(migrations)).filter(name => name.endsWith('.sql')).sort()) {
      await pool.query(await readFile(join(migrations, file), 'utf8'));
    }
    app = await createApp({ databaseUrl: 'postgresql://unused', adminKey: operatorKey, host: '127.0.0.1', port: 0 }, new PostgresNodeRepository(pool));
    await app.listen(0, '127.0.0.1');
    controllerUrl = await app.getUrl();
  });

  await stage('remote setup', async () => {
    successful(await powerShell(`New-Item -ItemType Directory -Path '${remoteDirectory}' | Out-Null`, { timeoutMs: 30_000 }), 'create remote directory');
    remoteCreated = true;
    successful(await powerShell(`$identity=$env:USERNAME + ':(OI)(CI)F'; icacls.exe '${remoteDirectory}' /inheritance:r /grant $identity '*S-1-5-18:(OI)(CI)F'; if ($LASTEXITCODE -ne 0) { throw 'ACL setup failed' }; Copy-Item -LiteralPath '${configuration.MESH_WINDOWS_BINARY}' -Destination '${remoteExecutable}'`, { timeoutMs: 120_000 }), 'copy remote binary');
    report.inventory = JSON.parse(successful(await ssh(`${remoteExecutable} info`, { timeoutMs: 30_000 }), 'agent info').stdout);
  });

  await stage('tunnels and enrollment', async () => {
    startProcess('ssh', [...sshArguments, '-o', 'ExitOnForwardFailure=yes', '-N', '-R',
      `127.0.0.1:17300:127.0.0.1:${new URL(controllerUrl).port}`, configuration.MESH_WINDOWS_HOST], { timeoutMs: 30 * 60_000 });
    dataTunnel = startProcess('ssh', [...sshArguments, '-o', 'ExitOnForwardFailure=yes', '-N', '-L',
      `127.0.0.1:${dataPort}:127.0.0.1:17332`, configuration.MESH_WINDOWS_HOST], { timeoutMs: 30 * 60_000 });
    const health = successful(await powerShell("$deadline=(Get-Date).AddSeconds(15); do { try { $status=(Invoke-WebRequest -UseBasicParsing 'http://127.0.0.1:17300/health/ready' -TimeoutSec 2).StatusCode } catch { $status=0 }; if ($status -eq 200) { Write-Output $status; exit 0 }; Start-Sleep -Milliseconds 100 } while ((Get-Date) -lt $deadline); throw 'control tunnel not ready'", { timeoutMs: 20_000 }), 'control tunnel health');
    assert.equal(health.stdout.trim(), '200');
    const token = z.object({ enrollmentToken: z.string().min(1) }).parse(await controllerRequest('/v1/enrollment-tokens', {})).enrollmentToken;
    successful(await ssh(`${remoteExecutable} enroll --server http://127.0.0.1:17300 --name "IdeaPad Windows Live" --token-stdin --state-dir ${remoteState}`, { stdin: token, timeoutMs: 45_000 }), 'enrollment');
    const before = z.object({ nodeId: z.uuid(), nextSequence: z.number().int() }).parse(JSON.parse(successful(await ssh(`${remoteExecutable} status --state-dir ${remoteState}`, { timeoutMs: 30_000 }), 'initial status').stdout));
    successful(await ssh(`${remoteExecutable} heartbeat --state-dir ${remoteState}`, { timeoutMs: 30_000 }), 'heartbeat one');
    successful(await ssh(`${remoteExecutable} heartbeat --state-dir ${remoteState}`, { timeoutMs: 30_000 }), 'heartbeat two');
    const after = z.object({ nodeId: z.uuid(), nextSequence: z.number().int() }).parse(JSON.parse(successful(await ssh(`${remoteExecutable} status --state-dir ${remoteState}`, { timeoutMs: 30_000 }), 'final status').stdout));
    assert.equal(after.nodeId, before.nodeId);
    assert.equal(after.nextSequence, before.nextSequence + 2);
    report.nodeId = after.nodeId;
    report.checks.push('Enrollment, DPAPI identity reload and persisted heartbeat sequence');
    servingProcess = await startStorage();
  });

  for (const fixture of [{ name: 'large', count: 1, size: 8 * 1024 * 1024 }, { name: 'small-files', count: 100, size: 4096 }]) {
    await stage(`${fixture.name} round trip`, async () => {
      const source = await createFixture(fixture.name, fixture.count, fixture.size);
      const expected = await tree(source);
      const copied = successful(await managed('copy', ['--source', source, '--name', fixture.name, '--json']), `${fixture.name} copy`);
      const collection = z.object({ id: z.string().regex(/^[a-f0-9]{64}$/) }).parse(JSON.parse(copied.stdout));
      report.transcripts[`${fixture.name}-copy`] = copied.stderr;
      report.measurements.push({ fixture: fixture.name, operation: 'copy', bytes: fixture.count * fixture.size,
        elapsedMs: copied.elapsedMs, firstLogMs: copied.firstLogMs });
      const destination = join(workDirectory, `${fixture.name}-download`);
      const retrieved = successful(await managed('get', ['--collection', fixture.name, '--destination', destination]), `${fixture.name} get`);
      report.transcripts[`${fixture.name}-get`] = retrieved.stderr;
      report.measurements.push({ fixture: fixture.name, operation: 'get', bytes: fixture.count * fixture.size,
        elapsedMs: retrieved.elapsedMs, firstLogMs: retrieved.firstLogMs });
      assert.deepEqual(await tree(destination), expected);
      const remoteHashes = successful(await powerShell(`$base='${remoteRoot}/collections/${collection.id}'; $hashes=@(Get-ChildItem -LiteralPath $base -Recurse -File | ForEach-Object { [PSCustomObject]@{path=$_.FullName.Substring($base.Length+1).Replace('\\','/');hash=(Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLower()} }); ConvertTo-Json -InputObject $hashes -Compress`, { timeoutMs: 120_000 }), 'remote hashes');
      const hashes = z.array(z.object({ path: z.string(), hash: z.string().regex(/^[a-f0-9]{64}$/) })).parse(JSON.parse(remoteHashes.stdout));
      assert.equal(hashes.length, fixture.count);
      for (const item of hashes) assert.equal(item.hash, expected[item.path]);
      const repeated = successful(await managed('copy', ['--source', source, '--name', fixture.name]), `${fixture.name} repeat copy`);
      assert.match(repeated.stderr, /already present/);
      report.measurements.push({ fixture: fixture.name, operation: 'repeat-copy', bytes: fixture.count * fixture.size,
        elapsedMs: repeated.elapsedMs, firstLogMs: repeated.firstLogMs });
      report.checks.push(`${fixture.name}: native and retrieved hashes match; repeat copy reused durable content`);
    });
  }

  if (configuration.MESH_WINDOWS_RECOVERY === '1') await stage('interruption and resume', async () => {
    const source = await createFixture('interrupted', 1, 16 * 1024 * 1024);
    const expected = await tree(source);
    let durableProgressObserved = false;
    let stopPromise;
    let stopError;
    let progressBuffer = '';
    const observeAcknowledgedProgress = text => {
      progressBuffer += text;
      const lines = progressBuffer.split(/\r?\n/);
      progressBuffer = lines.pop() ?? '';
      for (const line of lines) {
        const match = /^Uploading:\s+([0-9.]+) (B|KiB|MiB) \/ /.exec(line);
        if (!match) continue;
        const scale = { B: 1, KiB: 1024, MiB: 1024 * 1024 }[match[2]];
        const acknowledged = Number(match[1]) * scale;
        if (!durableProgressObserved && acknowledged >= 4 * 1024 * 1024) {
          durableProgressObserved = true;
          stopPromise = stopStorage().catch(error => { stopError = error; });
        }
      }
    };
    const interrupted = await managed('copy', ['--source', source, '--name', 'interrupted'], {
      timeoutMs: 5 * 60_000,
      onStderr: observeAcknowledgedProgress,
    });
    assert.equal(durableProgressObserved, true);
    await stopPromise;
    if (stopError) throw stopError;
    assert.notEqual(interrupted.code, 0);
    assert.match(interrupted.stderr, /rerun the same command/);
    report.transcripts.interrupted = interrupted.stderr;
    const pending = successful(await managed('catalog'), 'pending catalogue');
    assert.match(pending.stdout, /pending/);
    if (servingProcess) await servingProcess.done;
    servingProcess = await startStorage();
    const resumed = successful(await managed('copy', ['--source', source, '--name', 'interrupted']), 'resumed copy');
    assert.match(resumed.stderr, /already present/);
    report.transcripts.resumed = resumed.stderr;
    const destination = join(workDirectory, 'resumed-download');
    successful(await managed('get', ['--collection', 'interrupted', '--destination', destination]), 'resumed retrieval');
    assert.deepEqual(await tree(destination), expected);
    report.checks.push('Process termination after durable progress, pending catalogue, resumed bytes and full verified retrieval');
  });
  else {
    report.checks.push('SKIPPED: forced interruption and resume (MESH_WINDOWS_RECOVERY=0)');
    await saveReport();
  }

  await stage('revocation', async () => {
    assert.ok(report.nodeId);
    await controllerRequest(`/v1/nodes/${report.nodeId}/revoke`, {});
    const denied = await managed('get', ['--collection', 'large', '--destination', join(workDirectory, 'revoked')]);
    assert.notEqual(denied.code, 0);
    const heartbeat = await ssh(`${remoteExecutable} heartbeat --state-dir ${remoteState}`, { timeoutMs: 30_000 });
    assert.notEqual(heartbeat.code, 0);
    report.checks.push('Revoked node cannot obtain a read grant or heartbeat');
  });
  report.result = 'PASS';
} catch (error) {
  report.result = 'FAIL';
  report.error = error instanceof Error ? error.message : 'Windows live check failed';
  process.exitCode = 1;
} finally {
  try { await stopStorage(); } catch (error) { report.cleanupError = error instanceof Error ? error.message : 'Storage cleanup failed'; }
  for (const child of children) {
    if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM');
  }
  if (remoteCreated) {
    try {
      const cleanup = await powerShell(`if (Test-Path -LiteralPath '${remoteDirectory}') { Remove-Item -LiteralPath '${remoteDirectory}' -Recurse -Force }`, { timeoutMs: 120_000 });
      report.remoteCleanup = cleanup.code === 0;
      if (cleanup.code !== 0) report.cleanupError = cleanup.stderr || 'Remote cleanup failed';
    } catch (error) {
      report.remoteCleanup = false;
      report.cleanupError = error instanceof Error ? error.message : 'Remote cleanup failed';
    }
  }
  try { if (app) await app.close(); else await pool.end(); } catch (error) { report.cleanupError ??= error instanceof Error ? error.message : 'Controller cleanup failed'; }
  if (schemaCreated) {
    try { await admin.query(`DROP SCHEMA ${schema} CASCADE`); report.schemaCleanup = true; }
    catch (error) { report.schemaCleanup = false; report.cleanupError ??= error instanceof Error ? error.message : 'Schema cleanup failed'; }
  }
  await admin.end().catch(() => {});
  await rm(workDirectory, { recursive: true, force: true });
  await saveReport();
  console.info(`Report: ${reportPath} (${report.result})`);
}
