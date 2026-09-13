import { z } from 'zod';

function privateIPv4(value: string): boolean {
  const octets = value.split('.').map(Number);
  if (octets.length !== 4 || octets.some(part => !Number.isInteger(part) || part < 0 || part > 255)) return false;
  return octets[0] === 10 || (octets[0] === 172 && octets[1]! >= 16 && octets[1]! <= 31)
    || (octets[0] === 192 && octets[1] === 168);
}

export const directCandidateSchema = z.object({
  transport: z.literal('tcp'),
  host: z.ipv4().refine(privateIPv4, 'Direct candidates must use a private IPv4 address'),
  port: z.number().int().min(1024).max(65535),
}).strict();

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
  directCandidates: z.array(directCandidateSchema).max(8).default([])
    .refine(candidates => new Set(candidates.map(candidate => `${candidate.transport}/${candidate.host}:${candidate.port}`)).size === candidates.length,
      'Direct candidates must be unique'),
}).strict();

export type Enrollment = z.infer<typeof enrollSchema>;
export type Heartbeat = z.infer<typeof heartbeatSchema>;
export type DirectCandidate = z.infer<typeof directCandidateSchema>;
