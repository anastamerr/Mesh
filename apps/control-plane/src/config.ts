import { existsSync } from 'node:fs';
import { loadEnvFile } from 'node:process';
import { z } from 'zod';
import { BEARER_TOKEN_PATTERN } from './auth/tokens';

export const CONFIG = Symbol('CONFIG');
export interface Config {
  databaseUrl: string;
  adminKey: string;
  host: string;
  port: number;
}

export function loadEnvironment(): void {
  if (existsSync('.env')) loadEnvFile('.env');
}

const databaseSchema = z.object({
  DATABASE_URL: z.string().url().refine(v => /^postgres(ql)?:/.test(v)),
});
const configSchema = databaseSchema.extend({
  MESH_ADMIN_KEY: z.string().regex(BEARER_TOKEN_PATTERN).refine(v => !v.startsWith('replace-with')),
  HOST: z.string().default('127.0.0.1'),
  PORT: z.coerce.number().int().min(1).max(65535).default(3000),
});

function validated<T>(schema: z.ZodType<T>, environment: NodeJS.ProcessEnv): T {
  const result = schema.safeParse(environment);
  if (!result.success) {
    // Never include supplied environment values (credentials) in errors.
    throw new Error(`Invalid configuration: ${result.error.issues.map(i => i.path.join('.')).join(', ')}`);
  }
  return result.data;
}

export function readDatabaseUrl(environment = process.env): string {
  return validated(databaseSchema, environment).DATABASE_URL;
}

export function readConfig(environment = process.env): Config {
  const data = validated(configSchema, environment);
  return { databaseUrl: data.DATABASE_URL, adminKey: data.MESH_ADMIN_KEY,
    host: data.HOST, port: data.PORT };
}
