import { strict as assert } from 'node:assert';
import { execFile } from 'node:child_process';
import { createHash, randomUUID } from 'node:crypto';
import { readFile, readdir } from 'node:fs/promises';
import { resolve } from 'node:path';
import { test } from 'node:test';
import { promisify } from 'node:util';
import { Pool } from 'pg';

test('merged migrations upgrade both branch histories and rerun without changing data', {
  skip: !process.env.MESH_TEST_DATABASE_URL,
}, async t => {
  const connectionString = process.env.MESH_TEST_DATABASE_URL!;
  const admin = new Pool({ connectionString });
  const directory = resolve(__dirname, '../migrations');
  const files = (await readdir(directory)).filter(name => name.endsWith('.sql')).sort();
  try {
    for (const history of ['local-connectivity', 'incoming-mvp', 'fresh']) {
      await t.test(history, async () => {
        const schema = `mesh_upgrade_${randomUUID().replaceAll('-', '')}`;
        await admin.query(`CREATE SCHEMA ${schema}`);
        const pool = new Pool({ connectionString, options: `-c search_path=${schema}` });
        try {
          await pool.query('CREATE TABLE schema_migrations (name text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())');
          const prior = files.filter(name => history === 'local-connectivity'
            ? !['006_workloads.sql', '007_relay_routes.sql'].includes(name)
            : history === 'incoming-mvp' && name !== '006_direct_candidates.sql');
          for (const name of prior) {
            const sql = await readFile(resolve(directory, name), 'utf8');
            await pool.query(sql);
            await pool.query('INSERT INTO schema_migrations (name, checksum) VALUES ($1, $2)',
              [name, createHash('sha256').update(sql).digest('hex')]);
          }
          const url = new URL(connectionString);
          url.searchParams.set('options', `-c search_path=${schema}`);
          const env: NodeJS.ProcessEnv = { ...process.env, DATABASE_URL: url.toString() };
          delete env.DATABASE_URL_FILE;
          const migrate = () => promisify(execFile)(process.execPath,
            ['--import', 'tsx', resolve(__dirname, '../src/database/migrate.ts')],
            { env, timeout: 30_000 });
          await migrate();
          await pool.query("INSERT INTO enrollment_tokens (token_hash, expires_at) VALUES ('upgrade-sentinel', now()+interval '1 hour')");
          const second = await migrate();
          assert.equal(second.stdout.trim(), '', 'second migration run reapplied a migration');
          assert.equal((await pool.query('SELECT name FROM schema_migrations')).rowCount, files.length);
          assert.equal((await pool.query("SELECT token_hash FROM enrollment_tokens WHERE token_hash='upgrade-sentinel'")).rowCount, 1);
          await pool.query('SELECT direct_candidates FROM nodes LIMIT 0');
          await pool.query('SELECT route, workload_id FROM relay_tickets LIMIT 0');
          await pool.query('SELECT revision FROM workloads LIMIT 0');
        } finally {
          await pool.end();
          await admin.query(`DROP SCHEMA ${schema} CASCADE`);
        }
      });
    }
  } finally {
    await admin.end();
  }
});
