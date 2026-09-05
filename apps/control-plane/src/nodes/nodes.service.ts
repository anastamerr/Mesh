import { ConflictException, Inject, Injectable, NotFoundException, UnauthorizedException } from '@nestjs/common';
import { hashToken, newToken } from '../auth/tokens';
import { Enrollment, Heartbeat } from './contracts';
import { NODE_REPOSITORY, NodeRepository } from './repository';

@Injectable()
export class NodesService {
  constructor(@Inject(NODE_REPOSITORY) private readonly repository: NodeRepository) {}

  async createEnrollment() {
    const token = newToken('enroll');
    const expiresAt = new Date(Date.now() + 10 * 60 * 1000);
    await this.repository.createEnrollment(hashToken(token), expiresAt);
    return { enrollmentToken: token, expiresAt };
  }

  async enroll(input: Enrollment) {
    const credential = newToken('node');
    const credentialExpiresAt = new Date(Date.now() + 30 * 24 * 60 * 60 * 1000);
    const node = await this.repository.enroll(input, hashToken(input.enrollmentToken),
      hashToken(credential), credentialExpiresAt);
    if (!node) throw new UnauthorizedException('Enrollment token invalid, expired, or already used');
    return { node, nodeCredential: credential, credentialExpiresAt };
  }

  async heartbeat(nodeId: string, credential: string, input: Heartbeat) {
    const result = await this.repository.heartbeat(nodeId, hashToken(credential), input);
    if (result === 'unauthorized') throw new UnauthorizedException('Node credential invalid, expired, or revoked');
    if (result === 'stale') throw new ConflictException('Heartbeat sequence must increase');
    return { accepted: true };
  }

  async list() {
    const now = Date.now();
    return (await this.repository.list()).map(node => ({ ...node,
      status: node.revokedAt ? 'revoked' : !node.lastSeenAt ? 'unknown' :
        now - node.lastSeenAt.getTime() <= 60_000 ? 'online' : 'unreachable',
    }));
  }

  async revoke(nodeId: string) {
    if (!await this.repository.revoke(nodeId)) throw new NotFoundException('Node not found');
    return { revoked: true };
  }
}
