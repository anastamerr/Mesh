import { z } from 'zod';
import { strict as assert } from 'node:assert';
import { execFile, spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { promisify } from 'node:util';

// Exercises real controller HTTP, PostgreSQL grants, and the compiled storage CLI.
export async function storageFlow(options: {
  binary: string; directory: string; nodeId: string; url: string; operatorKey: string;
  whileControllerOffline: (check: () => Promise<void>) => Promise<void>;
}) {
  const { binary, directory, nodeId, url, operatorKey } = options;
  const run = promisify(execFile);
  const source = join(directory, 'source');
  await mkdir(source);
  const bytes = Buffer.alloc(8 * 1024 * 1024 + 17, 83);
  await writeFile(join(source, 'file.bin'), bytes);
  const { stdout } = await run(binary, ['storage', 'identify', '--source', source]);
  const id = stdout.trim();
  const root = join(directory, 'storage');
  const child = spawn(binary, ['storage', 'serve', '--enrolled', '--state-dir', directory,
    '--root', root, '--listen', '127.0.0.1:0'], { stdio: ['ignore', 'ignore', 'pipe'] });
  const exited = once(child, 'exit');
  try {
    const server = await new Promise<string>((resolve, reject) => {
      const timeout = setTimeout(() => reject(new Error('Storage startup timed out')), 10_000);
      let logs = '';
      child.stderr.on('data', data => {
        logs += data;
        const match = /Storage listening on ([^\r\n]+)/.exec(logs);
        if (match) { clearTimeout(timeout); resolve(`http://${match[1]}`); }
      });
      child.once('error', error => { clearTimeout(timeout); reject(error); });
      child.once('exit', () => { clearTimeout(timeout); reject(new Error('Storage exited before ready')); });
    });
    const headers = { authorization: `Bearer ${operatorKey}`, 'content-type': 'application/json' };
    async function grant(access: 'read' | 'write' | 'list') {
      const response = await fetch(`${url}/v1/nodes/${nodeId}/storage-grants`, {
        method: 'POST', headers, body: JSON.stringify({ access, collectionId: access === 'list' ? null : id }),
      });
      assert.equal(response.status, 201);
      const body = z.object({ token: z.string() }).parse(await response.json());
      const file = join(directory, `${access}.key`);
      await writeFile(file, body.token + '\n', { mode: 0o600 });
      return file;
    }
    // Also exercise the operator helper, which never passes the operator key to the agent.
    const writeKey = join(directory, 'write.key');
    const helper = resolve(__dirname, '../../../scripts/storage-grant-local.mjs');
    const helperResult = await run(process.execPath, [helper, '--node', nodeId, '--access', 'write', '--collection', id, '--out', writeKey],
      { env: { ...process.env, MESH_ADMIN_KEY: operatorKey, PORT: new URL(url).port } });
    assert.ok(!helperResult.stdout.includes(operatorKey));
    const command = (key: string, action: string, ...args: string[]) => run(binary,
      ['storage', action, '--server', server, '--key-file', key, ...args], { timeout: 20_000 });
    assert.equal((await command(writeKey, 'upload', '--source', source)).stdout.trim(), id);
    await assert.rejects(command(writeKey, 'list'), /HTTP 401/);
    const readKey = await grant('read');
    const destination = join(directory, 'download');
    await command(readKey, 'download', '--id', id, '--destination', destination);
    assert.deepEqual(await readFile(join(destination, 'file.bin')), bytes);
    await assert.rejects(command(readKey, 'upload', '--source', source), /HTTP 401/);
    await assert.rejects(command(readKey, 'download', '--id', 'a'.repeat(64), '--destination', join(directory, 'wrong')), /HTTP 401/);
    const listKey = await grant('list');
    assert.deepEqual(JSON.parse((await command(listKey, 'list')).stdout), [id]);
    const managedHelper = resolve(__dirname, '../../../scripts/mesh-local.mjs');
    const managed = (action: string, ...args: string[]) => run(process.execPath,
      [managedHelper, action, '--node', 'Integration host', ...args],
      { env: { ...process.env, MESH_ADMIN_KEY: operatorKey, PORT: new URL(url).port }, timeout: 20_000 });
    const copied = await managed('copy', '--server', server, '--source', source, '--name', 'Example folder', '--json');
    assert.equal(z.object({ id: z.string() }).parse(JSON.parse(copied.stdout)).id, id);
    assert.match(copied.stderr, /already present/);
    assert.match(copied.stderr, /Complete/);
    const catalogue = await managed('catalog');
    assert.match(catalogue.stdout, /Example folder/);
    assert.match(catalogue.stdout, /confirmed/);
    await managed('get', '--server', server, '--collection', 'Example folder', '--destination', join(directory, 'managed-download'));
    assert.deepEqual(await readFile(join(directory, 'managed-download', 'file.bin')), bytes);
    await options.whileControllerOffline(async () => {
      await assert.rejects(command(listKey, 'list'), /HTTP 503/);
    });
    assert.deepEqual(JSON.parse((await command(listKey, 'list')).stdout), [id]);
    assert.equal((await fetch(`${url}/v1/nodes/${nodeId}/revoke`, { method: 'POST', headers: { authorization: `Bearer ${operatorKey}` } })).status, 200);
    await assert.rejects(command(listKey, 'list'), /HTTP 401/);
    await assert.rejects(command(readKey, 'download', '--id', id, '--destination', join(directory, 'revoked')), /HTTP 401/);
    assert.deepEqual(await readFile(join(root, 'collections', id, 'file.bin')), bytes);
  } finally {
    if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL');
    await exited;
  }
}
