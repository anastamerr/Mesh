import { strict as assert } from 'node:assert';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { bearer } from '../src/auth/tokens';
import { loadSecretFiles, readConfig, readDatabaseUrl } from '../src/config';

test('configuration accepts only operator keys usable by the authentication guard', () => {
  const base = { DATABASE_URL: 'postgresql://localhost/mesh' };
  for (const key of ['a'.repeat(32), 'a'.repeat(256), 'mesh_TEST-123_'.repeat(4)]) {
    const config = readConfig({ ...base, MESH_ADMIN_KEY: key });
    assert.equal(bearer(`Bearer ${config.adminKey}`), key);
  }
  for (const key of ['a'.repeat(31), 'a'.repeat(257), 'x'.repeat(32) + '!', 'replace-with-random-characters-before-use']) {
    assert.throws(() => readConfig({ ...base, MESH_ADMIN_KEY: key }), /MESH_ADMIN_KEY/);
  }
});

test('container secret files are bounded and cannot ambiguously override environment values', t => {
  const directory = mkdtempSync(join(tmpdir(), 'mesh-config-'));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  const database = join(directory, 'database');
  const admin = join(directory, 'admin');
  writeFileSync(database, 'postgresql://mesh:secret@postgres/mesh\n', { mode: 0o600 });
  writeFileSync(admin, 'a'.repeat(43), { mode: 0o600 });
  const environment: NodeJS.ProcessEnv = { DATABASE_URL_FILE: database, MESH_ADMIN_KEY_FILE: admin };
  loadSecretFiles(environment);
  assert.equal(readConfig(environment).adminKey, 'a'.repeat(43));
  assert.equal(environment.DATABASE_URL, 'postgresql://mesh:secret@postgres/mesh');
  assert.throws(() => loadSecretFiles({ ...environment, DATABASE_URL_FILE: database }), /DATABASE_URL/);
  writeFileSync(admin, `valid\n${'b'.repeat(43)}`, { mode: 0o600 });
  assert.throws(() => loadSecretFiles({ DATABASE_URL: 'postgresql://localhost/mesh', MESH_ADMIN_KEY_FILE: admin }), /MESH_ADMIN_KEY_FILE/);
});

test('database migrations do not require operator credentials; invalid config does not leak secrets', () => {
  assert.equal(readDatabaseUrl({ DATABASE_URL: 'postgresql://localhost/mesh' }), 'postgresql://localhost/mesh');
  const secret = 'this-must-not-appear-in-errors';
  assert.throws(() => readConfig({ DATABASE_URL: `https://${secret}@localhost`, MESH_ADMIN_KEY: secret }), error => {
    assert.ok(error instanceof Error);
    assert.ok(!error.message.includes(secret));
    return true;
  });
});
