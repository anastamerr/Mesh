import { randomBytes } from 'node:crypto';
import { chmod, mkdir, readFile, rename, stat, writeFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = dirname(fileURLToPath(import.meta.url));
const secretsDirectory = join(root, 'secrets');
const generatedDirectory = join(root, 'generated');
const requiredSecrets = ['postgres_password', 'database_url', 'admin_key', 'relay_key'];

function hostname(name) {
  const value = process.env[name];
  if (!value || value.length > 253 || !/^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$/i.test(value) || value.includes('..')) {
    throw new Error(`${name} must be a DNS hostname`);
  }
  return value.toLowerCase();
}

function positiveInteger(name, fallback, maximum) {
  const raw = process.env[name] ?? String(fallback);
  if (!/^\d+$/.test(raw)) throw new Error(`${name} must be a positive integer`);
  const value = Number(raw);
  if (!Number.isSafeInteger(value) || value < 1 || value > maximum) throw new Error(`${name} is outside its safe range`);
  return value;
}

async function exists(path) {
  try { await stat(path); return true; } catch (error) {
    if (error?.code === 'ENOENT') return false;
    throw error;
  }
}

async function atomicWrite(path, content, mode) {
  const temporary = `${path}.${process.pid}.tmp`;
  await writeFile(temporary, content, { mode, flag: 'wx' });
  await rename(temporary, path);
  await chmod(path, mode);
}

async function validateSecret(path) {
  const info = await stat(path);
  if (!info.isFile() || info.size < 1 || info.size > 16 * 1024 || (process.platform !== 'win32' && (info.mode & 0o077) !== 0)) {
    throw new Error(`${path} must be a non-empty private regular file (0600)`);
  }
  const value = (await readFile(path, 'utf8')).trim();
  if (!value || value.includes('\n') || value.includes('\r')) throw new Error(`${path} must contain exactly one value`);
  return value;
}

const apiHost = hostname('MESH_API_HOST');
const relayHost = hostname('MESH_RELAY_HOST');
if (apiHost === relayHost) throw new Error('MESH_API_HOST and MESH_RELAY_HOST must be different DNS names');
const apiRate = positiveInteger('MESH_API_RATE_LIMIT_PER_MINUTE', 600, 100_000);
const pairingRate = positiveInteger('MESH_PAIRING_RATE_LIMIT_PER_MINUTE', 10, 10_000);

await mkdir(secretsDirectory, { recursive: true, mode: 0o700 });
await chmod(secretsDirectory, 0o700);
const present = await Promise.all(requiredSecrets.map(name => exists(join(secretsDirectory, name))));
if (present.some(Boolean) && !present.every(Boolean)) {
  throw new Error('Secret directory is incomplete; restore the missing files or remove the directory and bootstrap a fresh deployment');
}
if (!present.some(Boolean)) {
  const postgresPassword = randomBytes(36).toString('base64url');
  await atomicWrite(join(secretsDirectory, 'postgres_password'), `${postgresPassword}\n`, 0o600);
  await atomicWrite(join(secretsDirectory, 'database_url'), `postgresql://mesh:${postgresPassword}@postgres:5432/mesh\n`, 0o600);
  await atomicWrite(join(secretsDirectory, 'admin_key'), `${randomBytes(48).toString('base64url')}\n`, 0o600);
  await atomicWrite(join(secretsDirectory, 'relay_key'), `${randomBytes(48).toString('base64url')}\n`, 0o600);
}
const secretValues = new Map();
for (const name of requiredSecrets) secretValues.set(name, await validateSecret(join(secretsDirectory, name)));
if (secretValues.get('admin_key') === secretValues.get('relay_key')) throw new Error('Operator and relay credentials must differ');

await mkdir(generatedDirectory, { recursive: true, mode: 0o755 });
await chmod(generatedDirectory, 0o755);
const dynamic = `http:
  routers:
    controller-pairing:
      rule: "Host(\u0060${apiHost}\u0060) && Path(\u0060/v1/pairing-challenges\u0060) && Method(\u0060POST\u0060)"
      priority: 100
      entryPoints: [websecure]
      middlewares: [security-headers, pairing-rate]
      service: controller
      tls: { certResolver: letsencrypt, options: controller-modern }
    controller-relay-auth:
      rule: "Host(\u0060${apiHost}\u0060) && Path(\u0060/v1/relay/authorize\u0060) && Method(\u0060POST\u0060)"
      priority: 100
      entryPoints: [websecure]
      middlewares: [security-headers, relay-auth-rate]
      service: controller
      tls: { certResolver: letsencrypt, options: controller-modern }
    controller:
      rule: "Host(\u0060${apiHost}\u0060) && !PathPrefix(\u0060/metrics\u0060)"
      entryPoints: [websecure]
      middlewares: [security-headers, api-rate]
      service: controller
      tls: { certResolver: letsencrypt, options: controller-modern }
  middlewares:
    security-headers:
      headers:
        contentTypeNosniff: true
        frameDeny: true
        referrerPolicy: no-referrer
        stsSeconds: 31536000
        stsIncludeSubdomains: true
    api-rate:
      rateLimit: { average: ${apiRate}, period: 1m, burst: ${Math.max(20, Math.ceil(apiRate / 6))} }
    pairing-rate:
      rateLimit: { average: ${pairingRate}, period: 1m, burst: ${Math.max(2, Math.ceil(pairingRate / 3))} }
    relay-auth-rate:
      rateLimit: { average: 10000, period: 1m, burst: 1000 }
  services:
    controller:
      loadBalancer:
        servers: [{ url: "http://controller-proxy:3000" }]

tcp:
  routers:
    relay:
      rule: "HostSNI(\u0060${relayHost}\u0060)"
      entryPoints: [websecure]
      middlewares: [relay-inflight]
      service: relay
      tls: { certResolver: letsencrypt, options: relay-connect }
  middlewares:
    relay-inflight:
      inFlightConn: { amount: 2200 }
  services:
    relay:
      loadBalancer:
        servers: [{ address: "relay-proxy:7443" }]

tls:
  options:
    controller-modern:
      minVersion: VersionTLS12
      sniStrict: true
      alpnProtocols: [h2, http/1.1]
    relay-connect:
      minVersion: VersionTLS12
      sniStrict: true
      alpnProtocols: [http/1.1]
`;
await atomicWrite(join(generatedDirectory, 'traefik-dynamic.yml'), dynamic, 0o644);
const targets = [
  { targets: [`https://${apiHost}/health/ready`], labels: { module: 'http_2xx', probe: 'controller' } },
  { targets: [`${relayHost}:443`], labels: { module: 'tcp_tls', probe: 'relay' } },
];
await atomicWrite(join(generatedDirectory, 'prometheus-targets.json'), `${JSON.stringify(targets, null, 2)}\n`, 0o644);
console.log(`Production configuration prepared for https://${apiHost}/ and https://${relayHost}/`);
console.log('Secrets were preserved if they already existed; keep deploy/production/secrets backed up and private.');
