import { ConflictException, Inject, Injectable, NotFoundException, UnauthorizedException } from '@nestjs/common';
import { randomBytes } from 'node:crypto';
import { hashToken } from '../auth/tokens';
import type { CreateWorkload, DesiredState, ExecutionEnvironmentReport, WorkloadObservation } from './contracts';
import { WORKLOAD_REPOSITORY, WorkloadRepository } from './repository';

@Injectable()
export class WorkloadsService {
  constructor(@Inject(WORKLOAD_REPOSITORY) private readonly repository: WorkloadRepository) {}

  async create(input: CreateWorkload) {
    const result = await this.repository.createWorkload(input);
    if (result.kind === 'unavailable') {
      throw new ConflictException('Node runtime unavailable, workload capacity reached, or input collection is not confirmed on that node');
    }
    if (result.kind === 'conflict') throw new ConflictException('Workload ID already belongs to a different specification');
    return result.workload;
  }

  list() { return this.repository.listWorkloads(); }
  environments(nodeId: string) { return this.repository.listExecutionEnvironments(nodeId); }

  async reportEnvironment(nodeId: string, nodeCredentialHash: string, input: ExecutionEnvironmentReport) {
    const result = await this.repository.reportExecutionEnvironment(nodeId, nodeCredentialHash, input);
    if (!result) throw new UnauthorizedException('Node credential invalid, expired, or revoked');
    return result;
  }

  async setState(id: string, desiredState: DesiredState) {
    const result = await this.repository.setWorkloadState(id, desiredState);
    if (!result) throw new NotFoundException('Workload not found');
    if (result === 'invalid-state') throw new ConflictException('Run-to-completion jobs cannot be stopped through desired state');
    return result;
  }

  async assignments(nodeId: string, nodeCredentialHash: string) {
    const result = await this.repository.assignments(nodeId, nodeCredentialHash);
    if (!result) throw new UnauthorizedException('Node credential invalid, expired, or revoked');
    return result;
  }

  async observe(nodeId: string, nodeCredentialHash: string, workloadId: string, input: WorkloadObservation) {
    const result = await this.repository.observeWorkload(nodeId, nodeCredentialHash, workloadId, input);
    if (result === 'unauthorized') throw new UnauthorizedException('Node or workload assignment is invalid, expired, or revoked');
    if (result === 'stale') throw new ConflictException('Workload observation revision is stale or ahead of desired state');
    return { accepted: true };
  }

  async createDeviceTicket(nodeId: string, nodeCredentialHash: string, workloadId: string) {
    const token = randomBytes(32).toString('base64url');
    const ticket = await this.repository.createApplicationDeviceTicket(nodeId, nodeCredentialHash,
      workloadId, hashToken(token));
    if (!ticket) throw new UnauthorizedException('Application is not an active assignment for this node');
    return { ...ticket, token };
  }

  async createConsumerTicket(workloadId: string) {
    const token = randomBytes(32).toString('base64url');
    const ticket = await this.repository.createApplicationConsumerTicket(workloadId, hashToken(token));
    if (!ticket) throw new ConflictException('Application is not currently running');
    return { ...ticket, token };
  }
}
