import { z } from 'zod';
// Local operator helper. The agent receives only a scoped transfer token.
import { writeFile } from 'node:fs/promises';
import { parseArgs } from 'node:util';

async function main() {
  const { values } = parseArgs({ options: {
    node: { type: 'string' }, access: { type: 'string' }, collection: { type: 'string' }, out: { type: 'string' },
  } });
  if (!/^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$/.test(values.node ?? '')
    || !['read', 'write', 'list'].includes(values.access) || !values.out
    || (values.access === 'list' ? values.collection !== undefined : !/^[a-f0-9]{64}$/.test(values.collection ?? ''))) {
    throw new Error('Use --node UUID --access read|write|list --out NEW_FILE; read/write also require --collection ID.');
  }
  if (!process.env.MESH_ADMIN_KEY) throw new Error('Load the local controller environment first.');
  const port = Number(process.env.PORT || 3000);
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('Invalid local API port.');
  const response = await fetch(`http://127.0.0.1:${port}/v1/nodes/${values.node}/storage-grants`, {
    method: 'POST', headers: { authorization: `Bearer ${process.env.MESH_ADMIN_KEY}`, 'content-type': 'application/json' },
    body: JSON.stringify({ access: values.access, collectionId: values.collection ?? null }),
    signal: AbortSignal.timeout(10_000), redirect: 'error',
  });
  if (!response.ok) throw new Error(`Grant creation failed: HTTP ${response.status}.`);
  const parsed = z.object({ token: z.string().regex(/^[A-Za-z0-9_-]{43}$/), expiresAt: z.iso.datetime() })
    .safeParse(await response.json());
  if (!parsed.success) throw new Error('Invalid storage grant response.');
  const grant = parsed.data;
  await writeFile(values.out, grant.token + '\n', { mode: 0o600, flag: 'wx' });
  console.info(`Storage permission saved; expires at ${grant.expiresAt}.`);
}
main().catch(error => {
  console.error(error instanceof Error ? error.message : 'Storage grant creation failed.');
  process.exitCode = 1;
});
