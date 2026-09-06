import { registrationSchema, statisticsSchema } from './collection.contracts';
import { BadRequestException, Controller, Get, Headers, HttpCode, Inject, Param, ParseUUIDPipe, Post, Query, Req, UnauthorizedException, UseGuards } from '@nestjs/common';
import type { FastifyRequest } from 'fastify';
import { z } from 'zod';
import { AdminGuard } from '../auth/admin.guard';
import { bearer, hashToken } from '../auth/tokens';
import { parseBody } from '../http/parse-body';
import { STORAGE_REPOSITORY, StorageRepository } from './repository';

@Controller('v1/nodes/:id/collections')
export class CollectionsController {
  constructor(@Inject(STORAGE_REPOSITORY) private readonly storage: StorageRepository) {}

  @Post()
  @UseGuards(AdminGuard)
  async register(@Param('id', new ParseUUIDPipe({ version: '4' })) nodeId: string, @Req() request: FastifyRequest) {
    if (!await this.storage.registerCollection(nodeId, parseBody(registrationSchema, request))) {
      throw new UnauthorizedException('Node unavailable for storage');
    }
    return { accepted: true };
  }

  @Get()
  @UseGuards(AdminGuard)
  async list(@Param('id', new ParseUUIDPipe({ version: '4' })) nodeId: string, @Query('after') after?: string) {
    const cursor = z.string().regex(/^([a-f0-9]{64})?$/).safeParse(after ?? '');
    if (!cursor.success) throw new BadRequestException('Invalid collection cursor');
    return this.storage.listCollections(nodeId, cursor.data);
  }

  @Post('confirm')
  @HttpCode(200)
  async confirm(@Param('id', new ParseUUIDPipe({ version: '4' })) nodeId: string,
    @Headers('authorization') authorization: string | undefined, @Req() request: FastifyRequest) {
    if (!await this.storage.confirmCollection(nodeId, hashToken(bearer(authorization)), parseBody(statisticsSchema, request))) {
      throw new UnauthorizedException('Node credential invalid, expired, or revoked');
    }
    return { accepted: true };
  }
}
