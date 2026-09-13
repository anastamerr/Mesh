import type { CollectionRegistration, CollectionStatistics } from '../src/storage/collection.contracts';
import type { CollectionRecord } from '../src/storage/repository';
import { StoragePermission } from '../src/storage/contracts';
import { StorageRepository } from '../src/storage/repository';
import { randomUUID } from 'node:crypto';
import { Enrollment, Heartbeat } from '../src/nodes/contracts';
import { NodeRecord, NodeRepository } from '../src/nodes/repository';
import type { PairingRequest } from '../src/pairing/contracts';
import type { PairingApproval, PairingChallenge, PairingRepository, PairingStatus } from '../src/pairing/repository';
import type { CreateWorkload, DesiredState, ExecutionEnvironmentReport, WorkloadObservation } from '../src/workloads/contracts';
import type { ApplicationRelayTicket, ExecutionEnvironmentRecord, WorkloadCreation, WorkloadRecord,
  WorkloadRepository } from '../src/workloads/repository';

type StoredPairing = PairingChallenge & PairingRequest & { createdAt: Date; nodeId: string | null };

export class MemoryRepository implements NodeRepository, StorageRepository, PairingRepository, WorkloadRepository {
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
      revokedAt: null, inventory: null, publicKeyFingerprint: null, directCandidates: [], hash, expires, sequence: -1 };
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
    node.directCandidates = input.directCandidates;
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
  async createPairing(input: PairingRequest, code: string,
    expiresAt: Date): Promise<PairingChallenge | 'capacity' | 'conflict' | 'expired'> {
    const previous = this.pairings.get(input.pairingRequestId);
    if (previous) {
      const matches = previous.pairingSecretHash === input.pairingSecretHash
        && previous.nodeCredentialHash === input.nodeCredentialHash
        && previous.publicKeyFingerprint === input.publicKeyFingerprint && previous.name === input.name
        && previous.platform === input.platform && previous.architecture === input.architecture
        && previous.agentVersion === input.agentVersion;
      if (!matches) return 'conflict';
      if (!previous.nodeId && previous.expiresAt.getTime() <= Date.now()) return 'expired';
      return { id: previous.id, code: previous.code, expiresAt: previous.expiresAt };
    }
    const pending = [...this.pairings.values()].filter(pairing => !pairing.nodeId && pairing.expiresAt.getTime() > Date.now());
    if (pending.length >= 100) return 'capacity';
    const id = input.pairingRequestId;
    const challenge = { ...input, id, code, expiresAt, createdAt: new Date(), nodeId: null };
    this.pairings.set(id, challenge);
    return { id, code, expiresAt };
  }
  async listPendingPairings() {
    return [...this.pairings.values()].filter(pairing => !pairing.nodeId && pairing.expiresAt.getTime() > Date.now())
      .sort((a, b) => b.createdAt.getTime() - a.createdAt.getTime() || b.id.localeCompare(a.id)).slice(0, 100)
      .map(({ id, code, expiresAt, name, platform, architecture, agentVersion, publicKeyFingerprint, createdAt }) =>
        ({ id, code, expiresAt, name, platform, architecture, agentVersion, publicKeyFingerprint, createdAt }));
  }
  async approvePairing(id: string, expectedFingerprint: string, credentialExpiresAt: Date): Promise<PairingApproval> {
    const pairing = this.pairings.get(id);
    if (!pairing) return { kind: 'missing' };
    if (pairing.publicKeyFingerprint !== expectedFingerprint) return { kind: 'identity-conflict' };
    if (pairing.nodeId) {
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
      directCandidates: [], hash: pairing.nodeCredentialHash, expires: credentialExpiresAt, sequence: -1 };
    this.nodes.set(node.id, node);
    pairing.nodeId = node.id;
    return { kind: 'approved', node: this.publicNode(node), credentialExpiresAt };
  }
  async getPairingStatus(id: string, pairingSecretHash: string): Promise<PairingStatus> {
    const pairing = this.pairings.get(id);
    if (!pairing || pairing.pairingSecretHash !== pairingSecretHash) return { kind: 'unauthorized' };
    if (pairing.nodeId) {
      const node = this.nodes.get(pairing.nodeId);
      if (!node || node.revokedAt || node.expires.getTime() <= Date.now()) return { kind: 'expired' };
      return { kind: 'approved', node: this.publicNode(node), credentialExpiresAt: node.expires };
    }
    return pairing.expiresAt.getTime() <= Date.now() ? { kind: 'expired' } : { kind: 'pending' };
  }
  async getNodeConnection(nodeId: string) {
    const node = this.nodes.get(nodeId);
    if (!node?.publicKeyFingerprint || node.revokedAt || node.expires.getTime() <= Date.now()) return null;
    const recent = node.lastSeenAt && Date.now()-node.lastSeenAt.getTime() <= 60_000;
    return { nodeId, publicKeyFingerprint: node.publicKeyFingerprint,
      directCandidates: recent ? node.directCandidates : [], candidatesObservedAt: recent ? node.lastSeenAt : null };
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
  relayTickets = new Map<string, { role: 'device' | 'consumer'; nodeId: string; route: string;
    workloadId: string | null; expiresAt: Date }>();
  async createRelayTicket(role: 'device' | 'consumer', nodeId: string, sourceHash: string, ticketHash: string) {
    const node = this.nodes.get(nodeId);
    if (!node?.publicKeyFingerprint || node.revokedAt || node.expires.getTime() <= Date.now()) return null;
    const grant = role === 'consumer' ? this.grants.get(sourceHash) : undefined;
    if (role === 'device' ? node.hash !== sourceHash
      : !grant || grant.nodeId !== nodeId || grant.expiresAt.getTime() <= Date.now()) return null;
    const expiresAt = new Date(Math.min(node.expires.getTime(), grant?.expiresAt.getTime() ?? Infinity, Date.now() + 600000));
    for (const [hash, ticket] of this.relayTickets) if (ticket.expiresAt.getTime() <= Date.now()) this.relayTickets.delete(hash);
    this.relayTickets.set(ticketHash, { role, nodeId, route: 'storage', workloadId: null, expiresAt });
    return expiresAt;
  }
  async authorizeRelay(role: 'device' | 'consumer', nodeId: string, route: string, tokenHash: string) {
    const ticket = this.relayTickets.get(tokenHash), node = this.nodes.get(nodeId);
    const workload = ticket?.workloadId ? this.workloads.get(ticket.workloadId) : null;
    const appActive = !ticket?.workloadId || Boolean(workload && workload.nodeId === nodeId && workload.kind === 'application'
      && workload.desiredState === 'running' && workload.observedState === 'running'
      && workload.observedRevision === workload.revision && route === `app-${workload.id}`);
    if (!ticket || ticket.role !== role || ticket.nodeId !== nodeId || ticket.route !== route || ticket.expiresAt.getTime() <= Date.now()
      || !appActive
      || !node || node.revokedAt || node.expires.getTime() <= Date.now()) return null;
    const subject = route === 'storage' ? `${role}:${nodeId}` : `${role}:${nodeId}:${route}`;
    return { subject, expiresAt: ticket.expiresAt < node.expires ? ticket.expiresAt : node.expires };
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
  environments = new Map<string, ExecutionEnvironmentRecord>();
  async reportExecutionEnvironment(nodeId: string, nodeHash: string,
    input: ExecutionEnvironmentReport): Promise<ExecutionEnvironmentRecord | null> {
    const node = this.nodes.get(nodeId);
    if (!node || node.hash !== nodeHash || node.revokedAt || node.expires.getTime() <= Date.now()) return null;
    const key = `${nodeId}/${input.kind}`, existing = this.environments.get(key), now = new Date();
    const environment = { ...input, id: existing?.id ?? randomUUID(), nodeId,
      createdAt: existing?.createdAt ?? now, lastSeenAt: now };
    this.environments.set(key, environment);
    return environment;
  }
  async listExecutionEnvironments(nodeId: string) {
    return [...this.environments.values()].filter(environment => environment.nodeId === nodeId)
      .sort((left, right) => left.kind.localeCompare(right.kind) || left.id.localeCompare(right.id));
  }
  workloads = new Map<string, WorkloadRecord>();
  workloadCreations = new Map<string, string>();
  async createWorkload(input: CreateWorkload): Promise<WorkloadCreation> {
    const existing = this.workloads.get(input.id);
    if (existing) {
      return this.workloadCreations.get(input.id) === JSON.stringify(input)
        ? { kind: 'existing', workload: existing } : { kind: 'conflict' };
    }
    const node = this.nodes.get(input.nodeId), environment = this.environments.get(`${input.nodeId}/docker-linux`);
    const collection = input.inputCollectionId ? this.collections.get(`${input.nodeId}/${input.inputCollectionId}`) : null;
    const workloadCount = [...this.workloads.values()].filter(workload => workload.nodeId === input.nodeId &&
      (workload.kind === 'application' || (workload.observedState !== 'succeeded' && workload.observedState !== 'failed'))).length;
    if (!node || node.revokedAt || node.expires.getTime() <= Date.now() || environment?.status !== 'ready'
      || workloadCount >= 100 || (input.inputCollectionId !== null && !collection?.confirmedAt)) return { kind: 'unavailable' };
    const now = new Date();
    const workload: WorkloadRecord = { ...input, executionEnvironmentId: environment.id,
      revision: 1, observedRevision: null, observedState: 'pending',
      exitCode: null, failureCode: null, outputCollectionId: null, createdAt: now, updatedAt: now, observedAt: null };
    this.workloads.set(input.id, workload);
    this.workloadCreations.set(input.id, JSON.stringify(input));
    return { kind: 'created', workload };
  }
  async listWorkloads() {
    return [...this.workloads.values()].sort((a, b) => b.updatedAt.getTime() - a.updatedAt.getTime()
      || b.id.localeCompare(a.id)).slice(0, 100);
  }
  async setWorkloadState(id: string, desiredState: DesiredState) {
    const workload = this.workloads.get(id);
    if (!workload) return null;
    if (workload.kind === 'job') return desiredState === 'stopped' ? 'invalid-state' as const : workload;
    if (workload.desiredState === desiredState) return workload;
    const changed: WorkloadRecord = { ...workload, desiredState, revision: workload.revision + 1,
      observedRevision: null, observedState: 'pending', exitCode: null, failureCode: null,
      outputCollectionId: null, observedAt: null, updatedAt: new Date() };
    this.workloads.set(id, changed);
    return changed;
  }
  async assignments(nodeId: string, nodeHash: string) {
    const node = this.nodes.get(nodeId);
    if (!node || node.hash !== nodeHash || node.revokedAt || node.expires.getTime() <= Date.now()) return null;
    return [...this.workloads.values()].filter(workload => workload.nodeId === nodeId &&
      !(workload.kind === 'job' && (workload.observedState === 'succeeded' || workload.observedState === 'failed')))
      .sort((a, b) => a.createdAt.getTime() - b.createdAt.getTime() || a.id.localeCompare(b.id)).slice(0, 100);
  }
  async observeWorkload(nodeId: string, nodeHash: string, workloadId: string,
    input: WorkloadObservation): Promise<'accepted' | 'stale' | 'unauthorized'> {
    const node = this.nodes.get(nodeId), workload = this.workloads.get(workloadId);
    if (!node || node.hash !== nodeHash || node.revokedAt || node.expires.getTime() <= Date.now()
      || !workload || workload.nodeId !== nodeId) return 'unauthorized';
    const output = input.outputCollectionId ? this.collections.get(`${nodeId}/${input.outputCollectionId}`) : null;
    if (input.outputCollectionId !== null && !output?.confirmedAt) return 'stale';
    const rank = { pending: 0, pulling: 1, starting: 2, running: 3, exporting: 4, succeeded: 5, failed: 5, stopped: 5 } as const;
    const terminal = workload.observedState === 'succeeded' || workload.observedState === 'failed' || workload.observedState === 'stopped';
    const exactRepeat = workload.observedState === input.state && workload.exitCode === input.exitCode
      && workload.failureCode === input.failureCode && workload.outputCollectionId === input.outputCollectionId;
    const sameRevision = workload.observedRevision === input.revision;
    if (input.revision !== workload.revision
      || (sameRevision && ((terminal && !exactRepeat) || rank[input.state] < rank[workload.observedState]))) return 'stale';
    this.workloads.set(workloadId, { ...workload, observedRevision: input.revision, observedState: input.state,
      exitCode: input.exitCode, failureCode: input.failureCode, outputCollectionId: input.outputCollectionId, observedAt: new Date() });
    if (output && output.name === output.id) {
      this.collections.set(`${nodeId}/${output.id}`, { ...output, name: `${workload.name} output`.slice(0, 100) });
    }
    return 'accepted';
  }
  async createApplicationDeviceTicket(nodeId: string, nodeHash: string, workloadId: string,
    ticketHash: string): Promise<ApplicationRelayTicket | null> {
    const node = this.nodes.get(nodeId);
    if (!node || node.hash !== nodeHash) return null;
    return this.createApplicationTicket(workloadId, ticketHash, 'device');
  }
  async createApplicationConsumerTicket(workloadId: string, ticketHash: string): Promise<ApplicationRelayTicket | null> {
    return this.createApplicationTicket(workloadId, ticketHash, 'consumer');
  }
  private createApplicationTicket(workloadId: string, ticketHash: string,
    role: 'device' | 'consumer'): ApplicationRelayTicket | null {
    const workload = this.workloads.get(workloadId), node = workload ? this.nodes.get(workload.nodeId) : undefined;
    if (!workload || workload.kind !== 'application' || workload.desiredState !== 'running'
      || workload.observedState !== 'running' || workload.observedRevision !== workload.revision
      || !node?.publicKeyFingerprint || node.revokedAt || node.expires.getTime() <= Date.now()
      || workload.servicePort === null) return null;
    const expiresAt = new Date(Math.min(node.expires.getTime(), Date.now() + 600_000));
    const route = `app-${workload.id}`;
    this.relayTickets.set(ticketHash, { role, nodeId: workload.nodeId, route, workloadId, expiresAt });
    return { nodeId: workload.nodeId, route, servicePort: workload.servicePort,
      publicKeyFingerprint: node.publicKeyFingerprint, expiresAt };
  }
  async ready() { return this.available; }
}
