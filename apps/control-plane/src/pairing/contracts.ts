import { z } from 'zod';

const sha256Schema = z.string().regex(/^[a-f0-9]{64}$/);

export const createPairingSchema = z.object({
  pairingRequestId: z.uuid(),
  pairingSecretHash: sha256Schema,
  nodeCredentialHash: sha256Schema,
  publicKeyFingerprint: sha256Schema,
  name: z.string().trim().min(1).max(100),
  platform: z.enum(['windows', 'linux', 'darwin']),
  architecture: z.enum(['amd64', 'arm64']),
  agentVersion: z.string().min(1).max(40).regex(/^[A-Za-z0-9.+-]+$/),
}).strict();

export const approvePairingSchema = z.object({
  publicKeyFingerprint: sha256Schema,
}).strict();

export type PairingRequest = z.infer<typeof createPairingSchema>;
