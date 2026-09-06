import { createHash } from 'node:crypto';
import { readFile, readdir } from 'node:fs/promises';
import { resolve } from 'node:path';
import { createPool, transaction } from './connection';
import { loadEnvironment, readDatabaseUrl } from '../config';

async function main() {
  loadEnvironment();
  const pool = createPool(readDatabaseUrl());
  try {
    await transaction(pool, async client => {
      await client.query('SELECT pg_advisory_xact_lock(68435791)');
      await client.query(`CREATE TABLE IF NOT EXISTS schema_migrations
        (name text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`);
      const directory = resolve(__dirname, '../../migrations');
      const files = (await readdir(directory)).filter(file => /^\d+_[a-z_]+\.sql$/.test(file)).sort();
      for (const name of files) {
        const sql = await readFile(resolve(directory, name), 'utf8');
        const checksum = createHash('sha256').update(sql).digest('hex');
        const existing = await client.query<{ checksum: string }>('SELECT checksum FROM schema_migrations WHERE name = $1', [name]);
        if (existing.rows[0]) {
          if (existing.rows[0].checksum !== checksum) throw new Error(`Applied migration changed: ${name}`);
          continue;
        }
        await client.query(sql);
        await client.query('INSERT INTO schema_migrations (name, checksum) VALUES ($1, $2)', [name, checksum]);
        console.info(`Applied ${name}`);
      }
    });
  } finally { await pool.end(); }
}

main().catch(() => {
  console.error('Migration failed. Check database connectivity and migration integrity.');
  process.exitCode = 1;
});
