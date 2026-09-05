import { Body, Controller, Get, Headers, HttpCode, Inject, Param, ParseUUIDPipe, Post, UseGuards } from '@nestjs/common';
import { AdminGuard } from '../auth/admin.guard';
import { bearer } from '../auth/tokens';
import { enrollSchema, heartbeatSchema, parse } from './contracts';
import { NodesService } from './nodes.service';

@Controller('v1')
export class NodesController {
  constructor(@Inject(NodesService) private readonly nodes: NodesService) {}

  @Post('enrollment-tokens')
  @UseGuards(AdminGuard)
  createEnrollment() { return this.nodes.createEnrollment(); }

  @Post('nodes/enroll')
  enroll(@Body() body: unknown) { return this.nodes.enroll(parse(enrollSchema, body)); }

  @Get('nodes')
  @UseGuards(AdminGuard)
  list() { return this.nodes.list(); }

  @Post('nodes/:id/heartbeat')
  @HttpCode(200)
  heartbeat(@Param('id', new ParseUUIDPipe({ version: '4' })) id: string,
    @Headers('authorization') authorization: string | undefined, @Body() body: unknown) {
    return this.nodes.heartbeat(id, bearer(authorization), parse(heartbeatSchema, body));
  }

  @Post('nodes/:id/revoke')
  @HttpCode(200)
  @UseGuards(AdminGuard)
  revoke(@Param('id', new ParseUUIDPipe({ version: '4' })) id: string) { return this.nodes.revoke(id); }
}
