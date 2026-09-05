import { Controller, Get, Inject, ServiceUnavailableException } from '@nestjs/common';
import { NODE_REPOSITORY, NodeRepository } from './nodes/repository';

@Controller('health')
export class HealthController {
  constructor(@Inject(NODE_REPOSITORY) private readonly repository: NodeRepository) {}
  @Get('live')
  live() { return { status: 'ok' }; }
  @Get('ready')
  async ready() {
    if (!await this.repository.ready()) throw new ServiceUnavailableException('Database not ready');
    return { status: 'ok' };
  }
}
