import type { FastifyRequest } from 'fastify';
import { Controller, Get, Headers, HttpCode, Inject, Param, ParseUUIDPipe, Post, Req, UseGuards } from '@nestjs/common';
import { AdminGuard } from '../auth/admin.guard';
import { bearer } from '../auth/tokens';
import { parseBody } from '../http/parse-body';
import { approvePairingSchema, createPairingSchema } from './contracts';
import { PairingService } from './pairing.service';

@Controller('v1/pairing-challenges')
export class PairingController {
  constructor(@Inject(PairingService) private readonly pairing: PairingService) {}

  @Post()
  create(@Req() request: FastifyRequest) { return this.pairing.create(parseBody(createPairingSchema, request)); }

  @Get()
  @UseGuards(AdminGuard)
  list() { return this.pairing.list(); }

  @Post(':id/approve')
  @HttpCode(200)
  @UseGuards(AdminGuard)
  approve(@Param('id', new ParseUUIDPipe({ version: '4' })) id: string, @Req() request: FastifyRequest) {
    return this.pairing.approve(id, parseBody(approvePairingSchema, request).publicKeyFingerprint);
  }

  @Get(':id')
  status(@Param('id', new ParseUUIDPipe({ version: '4' })) id: string,
    @Headers('authorization') authorization: string | undefined) {
    return this.pairing.status(id, bearer(authorization));
  }
}
