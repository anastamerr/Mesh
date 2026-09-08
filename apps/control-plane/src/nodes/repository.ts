import { Enrollment, Heartbeat } from './contracts';

export const NODE_REPOSITORY = Symbol('NODE_REPOSITORY');
export interface NodeRecord {
  id: string;
  name: string;
  platform: Enrollment['platform'];
  architecture: Enrollment['architecture'];
  agentVersion: string;
  createdAt: Date;
  lastSeenAt: Date | null;
  revokedAt: Date | null;
  inventory: Heartbeat['inventory'] | null;
  publicKeyFingerprint: string | null;
}

export interface NodeRepository {
  createEnrollment(hash: string, expiresAt: Date): Promise<void>;
  enroll(input: Enrollment, enrollmentHash: string, credentialHash: string,
    credentialExpiresAt: Date): Promise<NodeRecord | null>;
  heartbeat(nodeId: string, credentialHash: string, input: Heartbeat): Promise<'accepted' | 'stale' | 'unauthorized'>;
  list(): Promise<NodeRecord[]>;
  revoke(nodeId: string): Promise<boolean>;
  renew(nodeId: string, credentialHash: string): Promise<Date | null>;
  getNodeConnection(nodeId: string): Promise<{ nodeId: string; publicKeyFingerprint: string } | null>;
  ready(): Promise<boolean>;
}
