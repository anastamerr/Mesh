import { BadRequestException } from '@nestjs/common';
import { z } from 'zod';

export const enrollSchema = z.object({
  enrollmentToken: z.string().regex(/^mesh_enroll_[A-Za-z0-9_-]{43}$/),
  name: z.string().trim().min(1).max(100),
  platform: z.enum(['windows', 'linux', 'darwin']),
  architecture: z.enum(['amd64', 'arm64']),
  agentVersion: z.string().min(1).max(40).regex(/^[A-Za-z0-9.+-]+$/),
}).strict();

export const heartbeatSchema = z.object({
  sequence: z.number().int().nonnegative().max(Number.MAX_SAFE_INTEGER),
  inventory: z.object({
    cpuLogicalCores: z.number().int().min(1).max(65536),
    memoryTotalBytes: z.number().int().positive().max(Number.MAX_SAFE_INTEGER),
    memoryAvailableBytes: z.number().int().nonnegative().max(Number.MAX_SAFE_INTEGER),
  }).strict().refine(v => v.memoryAvailableBytes <= v.memoryTotalBytes,
    'Available memory cannot exceed total memory'),
}).strict();

export type Enrollment = z.infer<typeof enrollSchema>;
export type Heartbeat = z.infer<typeof heartbeatSchema>;

export function parse<T>(schema: z.ZodType<T>, value: unknown): T {
  const result = schema.safeParse(value);
  if (!result.success) throw new BadRequestException({ message: 'Invalid request',
    issues: result.error.issues.map(i => ({ path: i.path.join('.'), message: i.message })) });
  return result.data;
}
