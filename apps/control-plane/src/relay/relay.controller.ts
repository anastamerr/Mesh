import type { FastifyRequest } from 'fastify';
import { CanActivate, Controller, ExecutionContext, Headers, HttpCode, Inject, Injectable, Param, ParseUUIDPipe, Post, Req, UnauthorizedException, UseGuards } from '@nestjs/common';
import { randomBytes } from 'node:crypto';
import { z } from 'zod';
import { bearer, hashToken, matchesSecret } from '../auth/tokens';
import { CONFIG, Config } from '../config';
import { parseBody } from '../http/parse-body';
import { NODE_REPOSITORY } from '../nodes/repository';

export interface RelayRepository {
  authorizeRelay(role: 'device' | 'consumer', nodeId: string, tokenHash: string): Promise<{
    subject: string;
    expiresAt: Date;
  } | null>;
  createRelayTicket(role: 'device' | 'consumer', nodeId: string, sourceHash: string, ticketHash: string): Promise<Date | null>;
}

const ticketSchema = z.object({ role: z.enum(['device', 'consumer']) }).strict();

@Controller('v1/nodes/:id/relay-tickets')
export class RelayTicketsController {
  constructor(@Inject(NODE_REPOSITORY) private readonly repository: RelayRepository) {}
  @Post()
  async create(@Param('id', new ParseUUIDPipe({ version: '4' })) nodeId: string,
    @Headers('authorization') authorization: string | undefined, @Req() request: FastifyRequest) {
    const { role } = parseBody(ticketSchema, request);
    const token = randomBytes(32).toString('base64url');
    const expiresAt = await this.repository.createRelayTicket(role, nodeId, hashToken(bearer(authorization)), hashToken(token));
    if (!expiresAt) throw new UnauthorizedException('Relay ticket source credential invalid');
    return { token, expiresAt };
  }
}

const requestSchema = z.object({
  role: z.enum(['device', 'consumer']),
  nodeId: z.string().uuid(),
  token: z.string().min(43).max(256).regex(/^[A-Za-z0-9_-]+$/),
}).strict();

@Injectable()
export class RelayGuard implements CanActivate {
  constructor(@Inject(CONFIG) private readonly config: Config) {}
  canActivate(context: ExecutionContext): boolean {
    const request = context.switchToHttp().getRequest<{ headers: { authorization?: string } }>();
    if (!this.config.relayKey || !matchesSecret(bearer(request.headers.authorization), this.config.relayKey)) {
      throw new UnauthorizedException('Invalid relay service credential');
    }
    return true;
  }
}

// Only the trusted relay calls this endpoint. Grant scope is still checked by
// the device for every storage request inside the end-to-end TLS connection.
@Controller('v1/relay')
@UseGuards(RelayGuard)
export class RelayController {
  constructor(@Inject(NODE_REPOSITORY) private readonly repository: RelayRepository) {}

  @Post('authorize')
  @HttpCode(200)
  async authorize(@Req() request: FastifyRequest) {
    const { role, nodeId, token } = parseBody(requestSchema, request);
    const lease = await this.repository.authorizeRelay(role, nodeId, hashToken(token));
    if (!lease) throw new UnauthorizedException('Relay access invalid, expired, or revoked');
    return lease;
  }
}
