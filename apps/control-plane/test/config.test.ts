import { strict as assert } from 'node:assert';
import { test } from 'node:test';
import { bearer } from '../src/auth/tokens';
import { readConfig, readDatabaseUrl } from '../src/config';

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

test('database migrations do not require operator credentials; invalid config does not leak secrets', () => {
  assert.equal(readDatabaseUrl({ DATABASE_URL: 'postgresql://localhost/mesh' }), 'postgresql://localhost/mesh');
  const secret = 'this-must-not-appear-in-errors';
  assert.throws(() => readConfig({ DATABASE_URL: `https://${secret}@localhost`, MESH_ADMIN_KEY: secret }), error => {
    assert.ok(error instanceof Error);
    assert.ok(!error.message.includes(secret));
    return true;
  });
});
