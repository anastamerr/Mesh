import { strict as assert } from 'node:assert';
import { spawn } from 'node:child_process';
import { createHash, randomBytes, randomUUID } from 'node:crypto';
import { createReadStream } from 'node:fs';
import { mkdir, mkdtemp, open, readFile, readdir, rm, stat, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { basename, dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const required = name => {
  const value = process.env[name]?.trim();
  if (!value) throw new Error(`Set ${name}`);
  return value;
};
const origin = (name, expectedHost) => {
  const url = new URL(required(name));
  if (url.protocol !== 'https:' || url.username || url.password || url.pathname !== '/' || url.search || url.hash) {
    throw new Error(`${name} must be an HTTPS origin ending in /`);
  }
  if (expectedHost && url.hostname !== expectedHost) throw new Error(`${name} does not match the deployed hostname`);
  return url.origin;
};

const controller = origin('MESH_PUBLIC_CONTROLLER');
const relay = origin('MESH_PUBLIC_RELAY');
const nodeSelector = required('MESH_PUBLIC_NODE');
const operatorFile = required('MESH_PUBLIC_OPERATOR_KEY_FILE');
const clientNetwork = required('MESH_PUBLIC_CLIENT_NETWORK');
const deviceNetwork = required('MESH_PUBLIC_DEVICE_NETWORK');
if (clientNetwork === deviceNetwork) throw new Error('Client and device network labels must be different');
const requestedBytes = Number(process.env.MESH_PUBLIC_TEST_BYTES ?? 256 * 1024 * 1024);
if (!Number.isSafeInteger(requestedBytes) || requestedBytes < 16 * 1024 * 1024 || requestedBytes > 4 * 1024 * 1024 * 1024) {
  throw new Error('MESH_PUBLIC_TEST_BYTES must be between 16 MiB and 4 GiB');
}
const defaultBinary = fileURLToPath(new URL('../../agent/bin/mesh-agent', import.meta.url));
const binary = process.env.MESH_PUBLIC_CLIENT_BINARY ?? defaultBinary;
const secretInfo = await stat(operatorFile);
if (!secretInfo.isFile() || secretInfo.size < 32 || secretInfo.size > 257 || (process.platform !== 'win32' && (secretInfo.mode & 0o077) !== 0)) {
  throw new Error('Operator key must be a small private regular file (0600 on Unix)');
}
const operator = (await readFile(operatorFile, 'utf8')).trim();
if (!/^[A-Za-z0-9_-]{32,256}$/.test(operator)) throw new Error('Operator key file is invalid');

const evidence = await mkdtemp(join(tmpdir(), 'mesh-public-validation-'));
const workspace = join(evidence, 'work');
const source = join(workspace, 'source');
const destination = join(workspace, 'retrieved');
const reportPath = join(evidence, 'report.json');
const report = {
  startedAt: new Date().toISOString(), result: 'RUNNING', controller, relay, nodeSelector,
  networks: { client: clientNetwork, device: deviceNetwork }, requestedBytes,
  checks: [], measurements: [], resume: { attempted: false, checkpointObserved: false },
};
const children = new Map();

async function save() { await writeFile(reportPath, `${JSON.stringify(report, null, 2)}\n`, { mode: 0o600 }); }

async function api(path, authenticated = false) {
  const headers = authenticated ? { authorization: `Bearer ${operator}` } : undefined;
  const response = await fetch(`${controller}${path}`, { headers, redirect: 'error', signal: AbortSignal.timeout(15_000) });
  const text = await response.text();
  assert.ok(response.ok, `GET ${path} returned HTTP ${response.status}`);
  return text ? JSON.parse(text) : null;
}

function start(args, timeoutMs = 60 * 60_000) {
  const began = performance.now();
  const child = spawn(binary, args, { stdio: ['pipe', 'pipe', 'pipe'], windowsHide: true });
  let stdout = '';
  let stderr = '';
  let settled = false;
  const timeout = setTimeout(() => {
    child.kill('SIGTERM');
    setTimeout(() => { if (!settled) child.kill('SIGKILL'); }, 5_000).unref();
  }, timeoutMs);
  const done = new Promise((resolve, reject) => {
    child.once('error', reject);
    child.stdout.on('data', chunk => { stdout += chunk; });
    child.stderr.on('data', chunk => { stderr += chunk; });
    child.once('close', (code, signal) => {
      settled = true;
      clearTimeout(timeout);
      children.delete(child);
      resolve({ code, signal, stdout, stderr, elapsedMs: performance.now() - began });
    });
  });
  child.stdin.on('error', () => {});
  child.stdin.end(operator);
  children.set(child, done);
  return { child, done };
}

async function successful(process, label) {
  const result = await process.done;
  assert.equal(result.code, 0, `${label} failed: ${result.stderr || result.stdout}`);
  return result;
}

async function digest(path) {
  const hash = createHash('sha256');
  for await (const chunk of createReadStream(path)) hash.update(chunk);
  return hash.digest('hex');
}

async function tree(path) {
  const result = {};
  async function visit(directory, prefix = '') {
    const entries = await readdir(directory, { withFileTypes: true });
    entries.sort((a, b) => a.name.localeCompare(b.name));
    for (const entry of entries) {
      const relative = `${prefix}${entry.name}`;
      if (entry.isDirectory()) {
        result[`${relative}/`] = 'directory';
        await visit(join(directory, entry.name), `${relative}/`);
      } else {
        result[relative] = await digest(join(directory, entry.name));
      }
    }
  }
  await visit(path);
  return result;
}

try {
  await mkdir(join(source, 'small'), { recursive: true });
  await mkdir(join(source, 'empty'), { recursive: true });
  const block = randomBytes(1024 * 1024);
  const large = await open(join(source, 'large.bin'), 'w', 0o600);
  try {
    for (let offset = 0; offset < requestedBytes; offset += block.length) {
      await large.write(block.subarray(0, Math.min(block.length, requestedBytes - offset)));
    }
    await large.sync();
  } finally { await large.close(); }
  for (let index = 0; index < 100; index++) {
    await writeFile(join(source, 'small', `${String(index).padStart(3, '0')}.bin`), randomBytes(4096), { mode: 0o600 });
  }

  await save();
  const health = await api('/health/ready');
  assert.equal(health.status, 'ok');
  const network = await api('/v1/network');
  assert.equal(network.relayOrigin, `${relay}/`);
  const metricsResponse = await fetch(`${controller}/metrics`, { redirect: 'error', signal: AbortSignal.timeout(15_000) });
  assert.equal(metricsResponse.status, 404, 'controller metrics must not be publicly routed');
  report.checks.push('Public HTTPS controller is ready, advertises the expected HTTPS relay, and does not expose metrics');

  const nodes = await api('/v1/nodes', true);
  const matches = nodes.filter(node => node.id === nodeSelector || node.name === nodeSelector);
  assert.equal(matches.length, 1, 'node selector must resolve to exactly one paired node');
  assert.equal(matches[0].status, 'online', 'paired node is not reporting online');
  report.nodeId = matches[0].id;
  report.checks.push('Physical device is paired and online on the declared device network');

  const common = ['--controller', controller, '--operator-stdin', '--node', report.nodeId];
  const collectionName = `Public WAN ${randomUUID()}`;
  const upload = await successful(start(['storage', 'copy', ...common, '--source', source, '--name', collectionName, '--json']), 'WAN upload');
  const collection = JSON.parse(upload.stdout);
  assert.match(collection.id, /^[a-f0-9]{64}$/);
  report.collectionId = collection.id;
  report.measurements.push({ operation: 'upload', bytes: collection.totalBytes, elapsedMs: upload.elapsedMs,
    mebibytesPerSecond: collection.totalBytes / 1048576 / (upload.elapsedMs / 1000) });
  report.checks.push('Large file, 100 small files, and an empty directory uploaded through the public relay');
  await save();

  const getArguments = ['storage', 'get', ...common, '--collection', collection.id, '--destination', destination];
  const firstDownload = start(getArguments);
  const journal = join(dirname(destination), `.${basename(destination)}.mesh-download`);
  const deadline = Date.now() + 120_000;
  while (Date.now() < deadline && firstDownload.child.exitCode === null && firstDownload.child.signalCode === null) {
    try {
      const journalText = await readFile(journal, 'utf8');
      if (journalText.split('\n').length >= 3) {
        report.resume.attempted = true;
        report.resume.checkpointObserved = true;
        firstDownload.child.kill('SIGTERM');
        break;
      }
    } catch (error) {
      if (error?.code !== 'ENOENT') throw error;
    }
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  const interrupted = await firstDownload.done;
  let download;
  if (report.resume.attempted) {
    assert.notEqual(interrupted.code, 0, 'the interruption did not stop the first download');
    download = await successful(start(getArguments), 'resumed WAN download');
    report.resume.succeeded = true;
  } else {
    assert.equal(interrupted.code, 0, `WAN download failed: ${interrupted.stderr}`);
    download = interrupted;
    report.resume.succeeded = false;
    report.resume.reason = 'transfer completed before a durable checkpoint could be interrupted';
  }
  assert.deepEqual(await tree(destination), await tree(source));
  report.measurements.push({ operation: report.resume.succeeded ? 'resumed download' : 'download', bytes: collection.totalBytes,
    elapsedMs: download.elapsedMs, mebibytesPerSecond: collection.totalBytes / 1048576 / (download.elapsedMs / 1000) });
  report.checks.push('Retrieved tree matches every source SHA-256 digest across the two declared physical networks');
  if (report.resume.succeeded) report.checks.push('Interrupted public-relay download resumed from a durable checkpoint');
  report.result = 'PASS';
  report.finishedAt = new Date().toISOString();
  await save();
  console.log(JSON.stringify(report, null, 2));
  console.log(`Evidence report: ${reportPath}`);
} catch (error) {
  report.result = 'FAIL';
  report.finishedAt = new Date().toISOString();
  report.error = error instanceof Error ? error.message : 'Unknown validation failure';
  await save();
  console.error(`Public deployment validation failed. Evidence report: ${reportPath}`);
  throw error;
} finally {
  for (const [child, done] of children) {
    child.kill('SIGKILL');
    await done.catch(() => {});
  }
  await rm(workspace, { recursive: true, force: true });
}
