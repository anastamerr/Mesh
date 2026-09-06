import { StoragePermission } from '../src/storage/contracts';
import { StorageGrantRepository } from '../src/storage/repository';
import { randomUUID } from 'node:crypto';
import { Enrollment, Heartbeat } from '../src/nodes/contracts';
import { NodeRecord, NodeRepository } from '../src/nodes/repository';

export class MemoryRepository implements NodeRepository, StorageGrantRepository {
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
      revokedAt: null, inventory: null, hash, expires, sequence: -1 };
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
  async ready() { return this.available; }
}
