import { Logger } from '@nestjs/common';
import { Pool, PoolClient } from 'pg';

const logger = new Logger('Database');

export function createPool(connectionString: string): Pool {
  const pool = new Pool({ connectionString, max: 10, connectionTimeoutMillis: 5000, statement_timeout: 5000 });
  // pg emits errors for idle clients outside requests. Handle them so a database
  // restart does not become an unhandled EventEmitter error that kills the API.
  pool.on('error', () => logger.error('Idle database connection failed; subsequent requests will reconnect'));
  return pool;
}

export async function transaction<T>(pool: Pool, work: (client: PoolClient) => Promise<T>): Promise<T> {
  const client = await pool.connect();
  let discard = false;
  try {
    await client.query('BEGIN');
    const result = await work(client);
    await client.query('COMMIT');
    return result;
  } catch (error) {
    try { await client.query('ROLLBACK'); }
    catch { discard = true; }
    // Preserve the original failure and never return a broken client to the pool.
    throw error;
  } finally {
    client.release(discard);
  }
}
