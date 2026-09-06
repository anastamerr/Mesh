// Development operator entry point: credentials go over stdin, never arguments.
import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const args = process.argv.slice(2);
if (!['copy', 'get', 'catalog'].includes(args[0])) {
  console.error('Usage: npm run mesh -- copy|get|catalog --node NAME [options]');
  process.exitCode = 1;
} else {
  const port = Number(process.env.PORT || 3000);
  if (!process.env.MESH_ADMIN_KEY || !Number.isInteger(port) || port < 1 || port > 65535) {
    console.error('Load the local controller environment with a valid operator key and port.');
    process.exitCode = 1;
  } else {
    const binary = fileURLToPath(new URL(`../agent/bin/mesh-agent${process.platform === 'win32' ? '.exe' : ''}`, import.meta.url));
    const env = { ...process.env };
    for (const key of ['MESH_ADMIN_KEY', 'DATABASE_URL', 'MESH_TEST_DATABASE_URL']) delete env[key];
    const child = spawn(binary, ['storage', ...args, '--controller', `http://127.0.0.1:${port}`, '--operator-stdin'],
      { env, stdio: ['pipe', 'inherit', 'inherit'] });
    child.stdin.on('error', () => { /* Child exit reports early validation failures. */ });
    child.once('error', () => { console.error('Could not start Mesh; build agent/bin/mesh-agent first.'); process.exitCode = 1; });
    child.once('close', code => { process.exitCode = code ?? 1; });
    for (const signal of ['SIGINT', 'SIGTERM']) process.once(signal, () => child.kill(signal));
    child.stdin.end(process.env.MESH_ADMIN_KEY);
  }
}
