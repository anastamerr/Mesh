import type { FastifyRequest } from 'fastify';
import { parseBody } from '../http/parse-body';
import { Req, Controller, Headers, HttpCode, Inject, Param, ParseUUIDPipe, Post, UnauthorizedException, UseGuards } from '@nestjs/common';
import { randomBytes } from 'node:crypto';
import { AdminGuard } from '../auth/admin.guard';
import { bearer, hashToken } from '../auth/tokens';
import { permissionSchema, validationSchema } from './contracts';
import { STORAGE_GRANTS, StorageGrantRepository } from './repository';

@Controller('v1/nodes/:id/storage-grants')
export class StorageController {
  constructor(@Inject(STORAGE_GRANTS) private readonly grants: StorageGrantRepository) {}

  @Post()
  @UseGuards(AdminGuard)
  async create(@Param('id', new ParseUUIDPipe({ version: '4' })) id: string, @Req() request: FastifyRequest) {
    const permission = parseBody(permissionSchema, request);
    const token = randomBytes(32).toString('base64url');
    const expiresAt = new Date(Date.now() + 10 * 60 * 1000);
    if (!await this.grants.createStorageGrant(id, hashToken(token), permission, expiresAt)) {
      throw new UnauthorizedException('Node unavailable for storage authorization');
    }
    return { token, expiresAt, nodeId: id, permission };
  }

  @Post('validate')
  @HttpCode(200)
  async validate(@Param('id', new ParseUUIDPipe({ version: '4' })) id: string,
    @Headers('authorization') authorization: string | undefined, @Req() request: FastifyRequest) {
    const nodeCredential = bearer(authorization);
    const { token, permission } = parseBody(validationSchema, request);
    if (!await this.grants.validateStorageGrant(id, hashToken(nodeCredential), hashToken(token), permission)) {
      throw new UnauthorizedException('Storage permission invalid, expired, or revoked');
    }
    return { accepted: true };
  }
}
