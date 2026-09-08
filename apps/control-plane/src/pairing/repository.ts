import type { NodeRecord } from '../nodes/repository';
import type { PairingRequest } from './contracts';

export const PAIRING_REPOSITORY = Symbol('PAIRING_REPOSITORY');

export interface PairingChallenge {
  id: string;
  code: string;
  expiresAt: Date;
}

export interface PendingPairing extends PairingChallenge {
  name: string;
  platform: PairingRequest['platform'];
  architecture: PairingRequest['architecture'];
  agentVersion: string;
  publicKeyFingerprint: string;
  createdAt: Date;
}

export type PairingStatus =
  | { kind: 'unauthorized' }
  | { kind: 'expired' }
  | { kind: 'pending' }
  | { kind: 'approved'; node: NodeRecord; credentialExpiresAt: Date };

export type PairingApproval =
  | { kind: 'missing' }
  | { kind: 'expired' }
  | { kind: 'identity-conflict' }
  | { kind: 'approved'; node: NodeRecord; credentialExpiresAt: Date };

export interface PairingRepository {
  createPairing(input: PairingRequest, code: string, expiresAt: Date):
    Promise<PairingChallenge | 'capacity' | 'conflict' | 'expired'>;
  listPendingPairings(): Promise<PendingPairing[]>;
  approvePairing(id: string, expectedFingerprint: string, credentialExpiresAt: Date): Promise<PairingApproval>;
  getPairingStatus(id: string, pairingSecretHash: string): Promise<PairingStatus>;
}
