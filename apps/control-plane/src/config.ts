import { existsSync } from 'node:fs';
import { loadEnvFile } from 'node:process';
import { z } from 'zod';

export const CONFIG = Symbol('CONFIG');
export interface Config {
  databaseUrl: string;
  adminKey: string;
  host: string;
  port: number;
}

export function readConfig(): Config {
  if (existsSync('.env')) loadEnvFile('.env');
  const result = z.object({
    DATABASE_URL: z.string().url().refine(v => /^postgres(ql)?:/.test(v)),
    MESH_ADMIN_KEY: z.string().min(32).refine(v => !v.startsWith('replace-with')),
    HOST: z.string().default('127.0.0.1'),
    PORT: z.coerce.number().int().min(1).max(65535).default(3000),
  }).safeParse(process.env);
  if (!result.success) {
    // Never include supplied environment values (credentials) in errors.
    throw new Error(`Invalid configuration: ${result.error.issues.map(i => i.path.join('.')).join(', ')}`);
  }
  return { databaseUrl: result.data.DATABASE_URL, adminKey: result.data.MESH_ADMIN_KEY,
    host: result.data.HOST, port: result.data.PORT };
}
