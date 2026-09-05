import 'reflect-metadata';
import { Module } from '@nestjs/common';
import { NestFactory } from '@nestjs/core';
import { FastifyAdapter, NestFastifyApplication } from '@nestjs/platform-fastify';
import { Pool } from 'pg';
import { CONFIG, Config } from './config';
import { AdminGuard } from './auth/admin.guard';
import { PostgresNodeRepository } from './database/postgres.repository';
import { HealthController } from './health.controller';
import { NodesController } from './nodes/nodes.controller';
import { NodesService } from './nodes/nodes.service';
import { NODE_REPOSITORY, NodeRepository } from './nodes/repository';

export async function createApp(config: Config, repository?: NodeRepository) {
  @Module({
    controllers: [HealthController, NodesController],
    providers: [
      { provide: CONFIG, useValue: config },
      { provide: NODE_REPOSITORY, useFactory: () => repository ?? new PostgresNodeRepository(new Pool({
        connectionString: config.databaseUrl, max: 10, connectionTimeoutMillis: 5000,
        statement_timeout: 5000,
      })) },
      AdminGuard, NodesService,
    ],
  })
  class AppModule {}

  const app = await NestFactory.create<NestFastifyApplication>(AppModule,
    new FastifyAdapter({ bodyLimit: 16 * 1024, trustProxy: false }), { logger: ['error', 'warn'] });
  app.getHttpAdapter().getInstance().addHook('onSend', async (_request, reply, payload) => {
    reply.header('Cache-Control', 'no-store');
    reply.header('X-Content-Type-Options', 'nosniff');
    return payload;
  });
  app.enableShutdownHooks();
  await app.init();
  return app;
}
