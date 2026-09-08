import { Controller, Get, Inject } from '@nestjs/common';
import { CONFIG, Config } from '../config';

@Controller('v1/network')
export class NetworkController {
  constructor(@Inject(CONFIG) private readonly config: Config) {}
  @Get()
  configuration() { return { relayOrigin: this.config.relayOrigin ?? null }; }
}
