import { strict as assert } from 'node:assert';
import { test } from 'node:test';
import { Pool } from 'pg';
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

test('transaction preserves its original failure and discards a client if rollback fails', async () => {
  const original = new Error('work failed');
  const commands: string[] = [];
  let released: boolean | undefined;
  const client = {
    query: async (sql: string) => {
      commands.push(sql);
      if (sql === 'ROLLBACK') throw new Error('connection lost');
    },
    release: (discard: boolean) => { released = discard; },
  };
  const pool = { connect: async () => client } as unknown as Pool;
  await assert.rejects(transaction(pool, async () => { throw original; }), error => error === original);
  assert.deepEqual(commands, ['BEGIN', 'ROLLBACK']);
  assert.equal(released, true);
});

test('successful transactions commit and return healthy clients to the pool', async () => {
  const commands: string[] = [];
  let released: boolean | undefined;
  const pool = { connect: async () => ({
    query: async (sql: string) => { commands.push(sql); },
    release: (discard: boolean) => { released = discard; },
  }) } as unknown as Pool;
  assert.equal(await transaction(pool, async () => 'result'), 'result');
  assert.deepEqual(commands, ['BEGIN', 'COMMIT']);
  assert.equal(released, false);
});
