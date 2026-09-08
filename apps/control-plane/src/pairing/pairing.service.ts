import { ConflictException, GoneException, HttpException, HttpStatus, Inject, Injectable, NotFoundException, UnauthorizedException } from '@nestjs/common';
import { randomBytes } from 'node:crypto';
import { hashToken } from '../auth/tokens';
import type { PairingRequest } from './contracts';
import { PAIRING_REPOSITORY, PairingRepository } from './repository';

const CODE_ALPHABET = 'ABCDEFGHJKLMNPQRSTUVWXYZ23456789';

function pairingCode(): string {
  const bytes = randomBytes(8);
  let code = '';
  for (const byte of bytes) code += CODE_ALPHABET[byte! % CODE_ALPHABET.length];
  return `${code.slice(0, 4)}-${code.slice(4)}`;
}

@Injectable()
export class PairingService {
  constructor(@Inject(PAIRING_REPOSITORY) private readonly repository: PairingRepository) {}

  async create(input: PairingRequest) {
    const result = await this.repository.createPairing(input, pairingCode(),
      new Date(Date.now() + 10 * 60 * 1000));
    if (result === 'capacity') throw new HttpException('Too many pending pairing challenges', HttpStatus.TOO_MANY_REQUESTS);
    if (result === 'conflict') throw new ConflictException('Pairing request identifier already used with different details');
    if (result === 'expired') throw new GoneException('Pairing challenge expired');
    return result;
  }

  list() { return this.repository.listPendingPairings(); }

  async approve(id: string, expectedFingerprint: string) {
    const result = await this.repository.approvePairing(id, expectedFingerprint,
      new Date(Date.now() + 30 * 24 * 60 * 60 * 1000));
    if (result.kind === 'missing') throw new NotFoundException('Pairing challenge not found');
    if (result.kind === 'expired') throw new GoneException('Pairing challenge expired');
    if (result.kind === 'identity-conflict') throw new ConflictException('Node identity or credential is already enrolled');
    return { node: result.node, credentialExpiresAt: result.credentialExpiresAt };
  }

  async status(id: string, secret: string) {
    const result = await this.repository.getPairingStatus(id, hashToken(secret));
    if (result.kind === 'unauthorized') throw new UnauthorizedException('Pairing proof invalid');
    if (result.kind === 'expired') throw new GoneException('Pairing challenge expired');
    if (result.kind === 'pending') return { status: 'pending' as const };
    return { status: 'approved' as const, node: result.node, credentialExpiresAt: result.credentialExpiresAt };
  }
}
