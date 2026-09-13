import { Controller, Get, Headers, HttpCode, Inject, Param, ParseUUIDPipe, Post, Req, UseGuards } from '@nestjs/common';
import type { FastifyRequest } from 'fastify';
import { AdminGuard } from '../auth/admin.guard';
import { bearer, hashToken } from '../auth/tokens';
import { parseBody } from '../http/parse-body';
import { createWorkloadSchema, desiredStateSchema, executionEnvironmentSchema, workloadObservationSchema } from './contracts';
import { WorkloadsService } from './workloads.service';

@Controller('v1')
export class WorkloadsController {
  constructor(@Inject(WorkloadsService) private readonly workloads: WorkloadsService) {}

  @Post('workloads')
  @UseGuards(AdminGuard)
  create(@Req() request: FastifyRequest) {
    return this.workloads.create(parseBody(createWorkloadSchema, request));
  }

  @Get('workloads')
  @UseGuards(AdminGuard)
  list() { return this.workloads.list(); }

  @Post('nodes/:nodeId/execution-environments')
  @HttpCode(200)
  reportEnvironment(@Param('nodeId', new ParseUUIDPipe({ version: '4' })) nodeId: string,
    @Headers('authorization') authorization: string | undefined, @Req() request: FastifyRequest) {
    return this.workloads.reportEnvironment(nodeId, hashToken(bearer(authorization)),
      parseBody(executionEnvironmentSchema, request));
  }

  @Get('nodes/:nodeId/execution-environments')
  @UseGuards(AdminGuard)
  environments(@Param('nodeId', new ParseUUIDPipe({ version: '4' })) nodeId: string) {
    return this.workloads.environments(nodeId);
  }

  @Post('workloads/:id/state')
  @HttpCode(200)
  @UseGuards(AdminGuard)
  setState(@Param('id', new ParseUUIDPipe({ version: '4' })) id: string, @Req() request: FastifyRequest) {
    return this.workloads.setState(id, parseBody(desiredStateSchema, request).desiredState);
  }

  @Get('nodes/:nodeId/workloads')
  assignments(@Param('nodeId', new ParseUUIDPipe({ version: '4' })) nodeId: string,
    @Headers('authorization') authorization: string | undefined) {
    return this.workloads.assignments(nodeId, hashToken(bearer(authorization)));
  }

  @Post('nodes/:nodeId/workloads/:workloadId/observations')
  @HttpCode(200)
  observe(@Param('nodeId', new ParseUUIDPipe({ version: '4' })) nodeId: string,
    @Param('workloadId', new ParseUUIDPipe({ version: '4' })) workloadId: string,
    @Headers('authorization') authorization: string | undefined, @Req() request: FastifyRequest) {
    return this.workloads.observe(nodeId, hashToken(bearer(authorization)), workloadId,
      parseBody(workloadObservationSchema, request));
  }

  @Post('nodes/:nodeId/workloads/:workloadId/relay-ticket')
  createDeviceTicket(@Param('nodeId', new ParseUUIDPipe({ version: '4' })) nodeId: string,
    @Param('workloadId', new ParseUUIDPipe({ version: '4' })) workloadId: string,
    @Headers('authorization') authorization: string | undefined) {
    return this.workloads.createDeviceTicket(nodeId, hashToken(bearer(authorization)), workloadId);
  }

  @Post('workloads/:id/connection-ticket')
  @UseGuards(AdminGuard)
  createConsumerTicket(@Param('id', new ParseUUIDPipe({ version: '4' })) id: string) {
    return this.workloads.createConsumerTicket(id);
  }
}
