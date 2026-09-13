import type { CreateWorkload, DesiredState, ExecutionEnvironmentReport, WorkloadObservation } from './contracts';

export const WORKLOAD_REPOSITORY = Symbol('WORKLOAD_REPOSITORY');

export type WorkloadRecord = CreateWorkload & {
  executionEnvironmentId: string;
  revision: number;
  observedRevision: number | null;
  observedState: 'pending' | WorkloadObservation['state'];
  exitCode: number | null;
  failureCode: WorkloadObservation['failureCode'];
  outputCollectionId: string | null;
  createdAt: Date;
  updatedAt: Date;
  observedAt: Date | null;
};

export interface ExecutionEnvironmentRecord extends ExecutionEnvironmentReport {
  id: string;
  nodeId: string;
  lastSeenAt: Date;
  createdAt: Date;
}

export type WorkloadCreation =
  | { kind: 'created'; workload: WorkloadRecord }
  | { kind: 'existing'; workload: WorkloadRecord }
  | { kind: 'conflict' }
  | { kind: 'unavailable' };

export type ApplicationRelayTicket = {
  nodeId: string;
  route: string;
  servicePort: number;
  publicKeyFingerprint: string;
  expiresAt: Date;
};

export interface WorkloadRepository {
  reportExecutionEnvironment(nodeId: string, nodeHash: string,
    input: ExecutionEnvironmentReport): Promise<ExecutionEnvironmentRecord | null>;
  listExecutionEnvironments(nodeId: string): Promise<ExecutionEnvironmentRecord[]>;
  createWorkload(input: CreateWorkload): Promise<WorkloadCreation>;
  listWorkloads(): Promise<WorkloadRecord[]>;
  setWorkloadState(id: string, desiredState: DesiredState): Promise<WorkloadRecord | 'invalid-state' | null>;
  assignments(nodeId: string, nodeHash: string): Promise<WorkloadRecord[] | null>;
  observeWorkload(nodeId: string, nodeHash: string, workloadId: string,
    input: WorkloadObservation): Promise<'accepted' | 'stale' | 'unauthorized'>;
  createApplicationDeviceTicket(nodeId: string, nodeHash: string, workloadId: string,
    ticketHash: string): Promise<ApplicationRelayTicket | null>;
  createApplicationConsumerTicket(workloadId: string, ticketHash: string): Promise<ApplicationRelayTicket | null>;
}
