import type { CollectionRegistration, CollectionStatistics } from '../src/storage/collection.contracts';
import type { CollectionRecord } from '../src/storage/repository';
import { StoragePermission } from '../src/storage/contracts';
import { StorageRepository } from '../src/storage/repository';
import { randomUUID } from 'node:crypto';
import { Enrollment, Heartbeat } from '../src/nodes/contracts';
import { NodeRecord, NodeRepository } from '../src/nodes/repository';
import type { PairingRequest } from '../src/pairing/contracts';
import type { PairingApproval, PairingChallenge, PairingRepository, PairingStatus } from '../src/pairing/repository';

type StoredPairing = PairingChallenge & PairingRequest & { createdAt: Date; approvedAt: Date | null;
  nodeId: string | null; credentialExpiresAt: Date | null };

export class MemoryRepository implements NodeRepository, StorageRepository, PairingRepository {
  tokens = new Map<string, Date>();
  nodes = new Map<string, NodeRecord & { hash: string; expires: Date; sequence: number }>();
  available = true;
  async createEnrollment(hash: string, expiresAt: Date) { this.tokens.set(hash, expiresAt); }
  async enroll(input: Enrollment, enrollmentHash: string, hash: string, expires: Date) {
    const token = this.tokens.get(enrollmentHash);
    if (!token || token.getTime() <= Date.now()) return null;
    this.tokens.delete(enrollmentHash);
    const { enrollmentToken: _secret, ...fields } = input;
    const node = { ...fields, id: randomUUID(), createdAt: new Date(), lastSeenAt: null,
      revokedAt: null, inventory: null, publicKeyFingerprint: null, hash, expires, sequence: -1 };
    this.nodes.set(node.id, node);
    return this.publicNode(node);
  }
  async heartbeat(id: string, hash: string, input: Heartbeat): Promise<'accepted' | 'stale' | 'unauthorized'> {
    const node = this.nodes.get(id);
    if (!node || node.hash !== hash || node.revokedAt || node.expires.getTime() <= Date.now()) return 'unauthorized';
    if (input.sequence <= node.sequence) return 'stale';
    node.sequence = input.sequence;
    node.lastSeenAt = new Date();
    node.inventory = input.inventory;
    return 'accepted';
  }
  private publicNode(node: NodeRecord & { hash: string; expires: Date; sequence: number }): NodeRecord {
    const { hash: _hash, expires: _expires, sequence: _sequence, ...record } = node;
    return record;
  }
  async list() {
    return [...this.nodes.values()]
      .sort((a, b) => b.createdAt.getTime() - a.createdAt.getTime() || b.id.localeCompare(a.id))
      .slice(0, 100)
      .map(node => this.publicNode(node));
  }
  async revoke(id: string) {
    const node = this.nodes.get(id);
    if (!node) return false;
    node.revokedAt ??= new Date();
    return true;
  }
  async renew(id: string, hash: string) {
    const node = this.nodes.get(id);
    if (!node || node.hash !== hash || !node.publicKeyFingerprint || node.revokedAt || node.expires.getTime() <= Date.now()) return null;
    if (node.expires.getTime() < Date.now() + 7 * 86400000) node.expires = new Date(Date.now() + 30 * 86400000);
    return node.expires;
  }
  pairings = new Map<string, StoredPairing>();
  async createPairing(input: PairingRequest, id: string, code: string,
    expiresAt: Date): Promise<PairingChallenge | 'capacity' | 'conflict' | 'expired'> {
    const previous = [...this.pairings.values()].find(pairing => pairing.pairingRequestId === input.pairingRequestId);
    if (previous) {
      const matches = previous.pairingSecretHash === input.pairingSecretHash
        && previous.nodeCredentialHash === input.nodeCredentialHash
        && previous.publicKeyFingerprint === input.publicKeyFingerprint && previous.name === input.name
        && previous.platform === input.platform && previous.architecture === input.architecture
        && previous.agentVersion === input.agentVersion;
      if (!matches) return 'conflict';
      if (!previous.approvedAt && previous.expiresAt.getTime() <= Date.now()) return 'expired';
      return { id: previous.id, code: previous.code, expiresAt: previous.expiresAt };
    }
    const pending = [...this.pairings.values()].filter(pairing => !pairing.approvedAt && pairing.expiresAt.getTime() > Date.now());
    if (pending.length >= 100) return 'capacity';
    const challenge = { ...input, id, code, expiresAt, createdAt: new Date(), approvedAt: null,
      nodeId: null, credentialExpiresAt: null };
    this.pairings.set(id, challenge);
    return { id, code, expiresAt };
  }
  async listPendingPairings() {
    return [...this.pairings.values()].filter(pairing => !pairing.approvedAt && pairing.expiresAt.getTime() > Date.now())
      .sort((a, b) => b.createdAt.getTime() - a.createdAt.getTime() || b.id.localeCompare(a.id)).slice(0, 100)
      .map(({ id, code, expiresAt, name, platform, architecture, agentVersion, publicKeyFingerprint, createdAt }) =>
        ({ id, code, expiresAt, name, platform, architecture, agentVersion, publicKeyFingerprint, createdAt }));
  }
  async approvePairing(id: string, expectedFingerprint: string, credentialExpiresAt: Date): Promise<PairingApproval> {
    const pairing = this.pairings.get(id);
    if (!pairing) return { kind: 'missing' };
    if (pairing.publicKeyFingerprint !== expectedFingerprint) return { kind: 'identity-conflict' };
    if (pairing.nodeId && pairing.credentialExpiresAt) {
      const node = this.nodes.get(pairing.nodeId);
      if (!node || node.revokedAt || node.expires.getTime() <= Date.now()) return { kind: 'expired' };
      return { kind: 'approved', node: this.publicNode(node), credentialExpiresAt: node.expires };
    }
    if (pairing.expiresAt.getTime() <= Date.now()) return { kind: 'expired' };
    if ([...this.nodes.values()].some(node => node.publicKeyFingerprint === pairing.publicKeyFingerprint
      || node.hash === pairing.nodeCredentialHash)) return { kind: 'identity-conflict' };
    const node = { id: randomUUID(), name: pairing.name, platform: pairing.platform,
      architecture: pairing.architecture, agentVersion: pairing.agentVersion, createdAt: new Date(), lastSeenAt: null,
      revokedAt: null, inventory: null, publicKeyFingerprint: pairing.publicKeyFingerprint,
      hash: pairing.nodeCredentialHash, expires: credentialExpiresAt, sequence: -1 };
    this.nodes.set(node.id, node);
    pairing.nodeId = node.id;
    pairing.approvedAt = new Date();
    pairing.credentialExpiresAt = credentialExpiresAt;
    return { kind: 'approved', node: this.publicNode(node), credentialExpiresAt };
  }
  async getPairingStatus(id: string, pairingSecretHash: string): Promise<PairingStatus> {
    const pairing = this.pairings.get(id);
    if (!pairing || pairing.pairingSecretHash !== pairingSecretHash) return { kind: 'unauthorized' };
    if (pairing.nodeId && pairing.credentialExpiresAt) {
      const node = this.nodes.get(pairing.nodeId);
      if (!node || node.revokedAt || node.expires.getTime() <= Date.now()) return { kind: 'expired' };
      return { kind: 'approved', node: this.publicNode(node), credentialExpiresAt: node.expires };
    }
    return pairing.expiresAt.getTime() <= Date.now() ? { kind: 'expired' } : { kind: 'pending' };
  }
  async getNodeConnection(nodeId: string) {
    const node = this.nodes.get(nodeId);
    if (!node?.publicKeyFingerprint || node.revokedAt || node.expires.getTime() <= Date.now()) return null;
    return { nodeId, publicKeyFingerprint: node.publicKeyFingerprint };
  }
  grants = new Map<string, { nodeId: string; permission: StoragePermission; expiresAt: Date }>();
  async createStorageGrant(nodeId: string, hash: string, permission: StoragePermission, expiresAt: Date) {
    const node = this.nodes.get(nodeId);
    if (!node || node.revokedAt || node.expires.getTime() <= Date.now()) return false;
    for (const [key, grant] of this.grants) if (grant.expiresAt.getTime() <= Date.now()) this.grants.delete(key);
    this.grants.set(hash, { nodeId, permission, expiresAt });
    return true;
  }
  async validateStorageGrant(nodeId: string, nodeHash: string, grantHash: string, permission: StoragePermission) {
    const node = this.nodes.get(nodeId), grant = this.grants.get(grantHash);
    return Boolean(this.available && node && node.hash === nodeHash && !node.revokedAt && node.expires.getTime() > Date.now()
      && grant && grant.nodeId === nodeId && grant.expiresAt.getTime() > Date.now()
      && grant.permission.access === permission.access && grant.permission.collectionId === permission.collectionId);
  }
  private async relaySource(role: 'device' | 'consumer', nodeId: string, tokenHash: string) {
    const node = this.nodes.get(nodeId);
    if (!node?.publicKeyFingerprint || node.revokedAt || node.expires.getTime() <= Date.now()) return null;
    if (role === 'device') return node.hash === tokenHash ? { subject: `device:${nodeId}`, expiresAt: node.expires } : null;
    const grant = this.grants.get(tokenHash);
    if (!grant || grant.nodeId !== nodeId || grant.expiresAt.getTime() <= Date.now()) return null;
    return { subject: `consumer:${nodeId}`, expiresAt: grant.expiresAt < node.expires ? grant.expiresAt : node.expires };
  }
  relayTickets = new Map<string, { role: 'device' | 'consumer'; nodeId: string; expiresAt: Date }>();
  async createRelayTicket(role: 'device' | 'consumer', nodeId: string, sourceHash: string, ticketHash: string) {
    const source = await this.relaySource(role, nodeId, sourceHash);
    if (!source) return null;
    const expiresAt = new Date(Math.min(source.expiresAt.getTime(), Date.now() + 600000));
    this.relayTickets.set(ticketHash, { role, nodeId, expiresAt });
    return expiresAt;
  }
  async authorizeRelay(role: 'device' | 'consumer', nodeId: string, tokenHash: string) {
    const ticket = this.relayTickets.get(tokenHash), node = this.nodes.get(nodeId);
    if (!ticket || ticket.role !== role || ticket.nodeId !== nodeId || ticket.expiresAt.getTime() <= Date.now()
      || !node || node.revokedAt || node.expires.getTime() <= Date.now()) return null;
    return { subject: `${role}:${nodeId}`, expiresAt: ticket.expiresAt < node.expires ? ticket.expiresAt : node.expires };
  }
  collections = new Map<string, CollectionRecord>();
  async registerCollection(nodeId: string, input: CollectionRegistration) {
    const node = this.nodes.get(nodeId);
    if (!node || node.revokedAt || node.expires.getTime() <= Date.now()) return false;
    const key = `${nodeId}/${input.id}`, existing = this.collections.get(key);
    this.collections.set(key, existing ? { ...existing, name: input.name } : { ...input, confirmedAt: null });
    return true;
  }
  async confirmCollection(nodeId: string, nodeHash: string, input: CollectionStatistics) {
    const node = this.nodes.get(nodeId);
    if (!node || node.hash !== nodeHash || node.revokedAt || node.expires.getTime() <= Date.now()) return false;
    const key = `${nodeId}/${input.id}`, existing = this.collections.get(key);
    this.collections.set(key, { ...input, name: existing?.name ?? input.id, confirmedAt: new Date() });
    return true;
  }
  async listCollections(nodeId: string, after: string) {
    return [...this.collections.entries()].filter(([key, item]) => key.startsWith(`${nodeId}/`) && item.id > after)
      .map(([, item]) => item).sort((a, b) => a.id.localeCompare(b.id)).slice(0, 100);
  }
  async ready() { return this.available; }
}
