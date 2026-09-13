import type { Pool } from 'pg';
import type { CreateWorkload, DesiredState, ExecutionEnvironmentReport, WorkloadObservation } from '../workloads/contracts';
import type { ApplicationRelayTicket, ExecutionEnvironmentRecord, WorkloadCreation, WorkloadRecord,
  WorkloadRepository } from '../workloads/repository';
import { PostgresNodeRepository } from './postgres.repository';
import { PostgresWorkloadRepository } from './postgres.workload-repository';

// Test and embedded callers can use one repository value while the production
// implementation remains split by domain behind this small composition seam.
export class PostgresAppRepository extends PostgresNodeRepository implements WorkloadRepository {
  private readonly workloadRepository: PostgresWorkloadRepository;

  constructor(pool: Pool) {
    super(pool);
    this.workloadRepository = new PostgresWorkloadRepository(pool);
  }

  reportExecutionEnvironment(nodeId: string, nodeHash: string,
    input: ExecutionEnvironmentReport): Promise<ExecutionEnvironmentRecord | null> {
    return this.workloadRepository.reportExecutionEnvironment(nodeId, nodeHash, input);
  }

  listExecutionEnvironments(nodeId: string): Promise<ExecutionEnvironmentRecord[]> {
    return this.workloadRepository.listExecutionEnvironments(nodeId);
  }

  createWorkload(input: CreateWorkload): Promise<WorkloadCreation> {
    return this.workloadRepository.createWorkload(input);
  }

  listWorkloads(): Promise<WorkloadRecord[]> {
    return this.workloadRepository.listWorkloads();
  }

  setWorkloadState(id: string, desiredState: DesiredState): Promise<WorkloadRecord | 'invalid-state' | null> {
    return this.workloadRepository.setWorkloadState(id, desiredState);
  }

  assignments(nodeId: string, nodeHash: string): Promise<WorkloadRecord[] | null> {
    return this.workloadRepository.assignments(nodeId, nodeHash);
  }

  observeWorkload(nodeId: string, nodeHash: string, workloadId: string,
    input: WorkloadObservation): Promise<'accepted' | 'stale' | 'unauthorized'> {
    return this.workloadRepository.observeWorkload(nodeId, nodeHash, workloadId, input);
  }

  createApplicationDeviceTicket(nodeId: string, nodeHash: string, workloadId: string,
    ticketHash: string): Promise<ApplicationRelayTicket | null> {
    return this.workloadRepository.createApplicationDeviceTicket(nodeId, nodeHash, workloadId, ticketHash);
  }

  createApplicationConsumerTicket(workloadId: string, ticketHash: string): Promise<ApplicationRelayTicket | null> {
    return this.workloadRepository.createApplicationConsumerTicket(workloadId, ticketHash);
  }
}
