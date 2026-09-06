import { randomUUID } from 'node:crypto';
import { Enrollment, Heartbeat } from '../src/nodes/contracts';
import { NodeRecord, NodeRepository } from '../src/nodes/repository';

export class MemoryRepository implements NodeRepository {
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
  async ready() { return this.available; }
}
