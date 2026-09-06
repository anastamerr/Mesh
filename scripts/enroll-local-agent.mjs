// Development-only bridge: keeps the operator key and enrollment token out of
// shell arguments/history. The native agent never receives the operator key.
import { spawn } from 'node:child_process';
import { access } from 'node:fs/promises';
import { hostname } from 'node:os';
import { fileURLToPath } from 'node:url';
import { parseArgs } from 'node:util';

async function main() {
  const { values } = parseArgs({ options: {
    name: { type: 'string', default: hostname() },
    'state-dir': { type: 'string' },
  } });
  if (!process.env.MESH_ADMIN_KEY) throw new Error('Load the control-plane .env before running this helper.');
  const binary = fileURLToPath(new URL(`../agent/bin/mesh-agent${process.platform === 'win32' ? '.exe' : ''}`, import.meta.url));
  await access(binary);
  const port = Number(process.env.PORT || 3000);
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('Invalid local API port.');
  const server = `http://127.0.0.1:${port}`;
  const response = await fetch(`${server}/v1/enrollment-tokens`, {
    method: 'POST', headers: { authorization: `Bearer ${process.env.MESH_ADMIN_KEY}` },
    signal: AbortSignal.timeout(10_000), redirect: 'error',
  });
  if (!response.ok) throw new Error(`Token creation failed: HTTP ${response.status}.`);
  const { enrollmentToken } = await response.json();
  if (typeof enrollmentToken !== 'string' || !/^mesh_enroll_[A-Za-z0-9_-]{43}$/.test(enrollmentToken)) {
    throw new Error('Invalid enrollment response.');
  }
  const args = ['enroll', '--server', server, '--name', values.name, '--token-stdin'];
  if (values['state-dir']) args.push('--state-dir', values['state-dir']);
  const env = { ...process.env };
  for (const key of ['MESH_ADMIN_KEY', 'DATABASE_URL', 'MESH_TEST_DATABASE_URL']) delete env[key];
  await new Promise((resolve, reject) => {
    const child = spawn(binary, args, { env, stdio: ['pipe', 'inherit', 'inherit'] });
    child.once('error', reject);
    child.stdin.on('error', () => { /* Exit status below reports early agent failures. */ });
    child.once('close', code => code === 0 ? resolve() : reject(new Error('Agent enrollment failed; inspect the message above.')));
    child.stdin.end(enrollmentToken);
  });
}

main().catch(error => {
  // Do not dump request objects or environment values.
  console.error(error instanceof Error ? error.message : 'Local enrollment failed.');
  process.exitCode = 1;
});
