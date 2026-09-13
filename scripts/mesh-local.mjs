// Development operator entry point: credentials go over stdin, never arguments.
import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const args = process.argv.slice(2);
const storageCommands = new Set(['copy', 'get', 'catalog']);
const workloadCommands = new Map([
  ['job', 'create-job'], ['app', 'create-app'], ['workloads', 'list'], ['readiness', 'environments'], ['start', 'start'], ['stop', 'stop'],
  ['connect', 'connect'],
]);
if (!storageCommands.has(args[0]) && !workloadCommands.has(args[0])) {
  console.error('Usage: npm run mesh -- copy|get|catalog|job|app|workloads|readiness|start|stop|connect [options]');
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
    const command = storageCommands.has(args[0]) ? ['storage', ...args] : ['workload', workloadCommands.get(args[0]), ...args.slice(1)];
    const child = spawn(binary, [...command, '--controller', `http://127.0.0.1:${port}`, '--operator-stdin'],
      { env, stdio: ['pipe', 'inherit', 'inherit'] });
    child.stdin.on('error', () => { /* Child exit reports early validation failures. */ });
    child.once('error', () => { console.error('Could not start Mesh; build agent/bin/mesh-agent first.'); process.exitCode = 1; });
    child.once('close', code => { process.exitCode = code ?? 1; });
    for (const signal of ['SIGINT', 'SIGTERM']) process.once(signal, () => child.kill(signal));
    child.stdin.end(process.env.MESH_ADMIN_KEY);
  }
}
