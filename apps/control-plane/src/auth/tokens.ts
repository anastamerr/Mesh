import { createHash, randomBytes, timingSafeEqual } from 'node:crypto';
import { UnauthorizedException } from '@nestjs/common';

export function hashToken(value: string): string {
  return createHash('sha256').update(value).digest('hex');
}

export function newToken(prefix: 'enroll' | 'node'): string {
  return `mesh_${prefix}_${randomBytes(32).toString('base64url')}`;
}

export function bearer(header: string | undefined): string {
  if (!header || !/^Bearer [A-Za-z0-9_-]{32,256}$/.test(header)) {
    throw new UnauthorizedException('Valid bearer credential required');
  }
  return header.slice(7);
}

export function matchesSecret(left: string, right: string): boolean {
  return timingSafeEqual(Buffer.from(hashToken(left), 'hex'), Buffer.from(hashToken(right), 'hex'));
}
