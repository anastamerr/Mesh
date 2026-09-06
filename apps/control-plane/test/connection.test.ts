import { strict as assert } from 'node:assert';
import { test } from 'node:test';
import { Client, Pool } from 'pg';
import { Logger } from '@nestjs/common';
import { createPool, transaction } from '../src/database/connection';

test('idle database errors are contained without logging connection details', async t => {
  const log = t.mock.method(Logger.prototype, 'error', () => {});
  const pool = createPool('postgresql://unused');
  assert.doesNotThrow(() => pool.emit('error', new Error('secret connection details')));
  assert.equal(log.mock.callCount(), 1);
  assert.ok(!JSON.stringify(log.mock.calls[0]?.arguments).includes('secret connection details'));
  await pool.end();
});

test('transaction preserves its original failure and discards a client if rollback fails', async t => {
  const original = new Error('work failed');
  const commands: string[] = [];
  let released: boolean | undefined;
  const client = Object.assign(new Client(), { release: (discard?: boolean) => { released = discard; } });
  t.mock.method(client, 'query', async (sql: string) => {
    commands.push(sql);
    if (sql === 'ROLLBACK') throw new Error('connection lost');
  });
  const pool = new Pool();
  t.after(() => pool.end());
  t.mock.method(pool, 'connect', async () => client);
  await assert.rejects(transaction(pool, async () => { throw original; }), error => error === original);
  assert.deepEqual(commands, ['BEGIN', 'ROLLBACK']);
  assert.equal(released, true);
});

test('successful transactions commit and return healthy clients to the pool', async t => {
  const commands: string[] = [];
  let released: boolean | undefined;
  const client = Object.assign(new Client(), { release: (discard?: boolean) => { released = discard; } });
  t.mock.method(client, 'query', async (sql: string) => { commands.push(sql); });
  const pool = new Pool();
  t.after(() => pool.end());
  t.mock.method(pool, 'connect', async () => client);
  assert.equal(await transaction(pool, async () => 'result'), 'result');
  assert.deepEqual(commands, ['BEGIN', 'COMMIT']);
  assert.equal(released, false);
});
