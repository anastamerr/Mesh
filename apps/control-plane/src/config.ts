import { existsSync } from 'node:fs';
import { loadEnvFile } from 'node:process';
import { z } from 'zod';
import { BEARER_TOKEN_PATTERN } from './auth/tokens';

export const CONFIG = Symbol('CONFIG');
export interface Config {
  databaseUrl: string;
  adminKey: string;
  relayKey?: string;
  relayOrigin?: string;
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
  MESH_RELAY_KEY: z.string().regex(BEARER_TOKEN_PATTERN).refine(v => !v.startsWith('replace-with')).optional(),
  MESH_RELAY_ORIGIN: z.url().refine(value => {
    const url = new URL(value);
    return url.protocol === 'https:' && !url.username && !url.password && !url.search && !url.hash && url.pathname === '/';
  }).optional(),
  HOST: z.string().default('127.0.0.1'),
  PORT: z.coerce.number().int().min(1).max(65535).default(3000),
}).refine(data => !data.MESH_RELAY_KEY || data.MESH_RELAY_KEY !== data.MESH_ADMIN_KEY,
  { path: ['MESH_RELAY_KEY'], message: 'Relay and operator credentials must differ' })
  .refine(data => !data.MESH_RELAY_ORIGIN || Boolean(data.MESH_RELAY_KEY),
    { path: ['MESH_RELAY_KEY'], message: 'A published relay requires a service credential' });

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
  return { databaseUrl: data.DATABASE_URL, adminKey: data.MESH_ADMIN_KEY, relayKey: data.MESH_RELAY_KEY, relayOrigin: data.MESH_RELAY_ORIGIN,
    host: data.HOST, port: data.PORT };
}
