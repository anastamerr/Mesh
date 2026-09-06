import 'reflect-metadata';
import { CollectionsController } from './storage/collections.controller';
import { STORAGE_REPOSITORY, StorageRepository } from './storage/repository';
import { StorageController } from './storage/storage.controller';
import { Module } from '@nestjs/common';
import { NestFactory } from '@nestjs/core';
import { FastifyAdapter, NestFastifyApplication } from '@nestjs/platform-fastify';
import { createPool } from './database/connection';
import { CONFIG, Config } from './config';
import { AdminGuard } from './auth/admin.guard';
import { PostgresNodeRepository } from './database/postgres.repository';
import { HealthController } from './health.controller';
import { NodesController } from './nodes/nodes.controller';
import { NodesService } from './nodes/nodes.service';
import { NODE_REPOSITORY, NodeRepository } from './nodes/repository';

export async function createApp(config: Config, repository?: NodeRepository & StorageRepository) {
  @Module({
    controllers: [HealthController, NodesController, StorageController, CollectionsController],
    providers: [
      { provide: CONFIG, useValue: config },
      { provide: NODE_REPOSITORY, useFactory: () => repository ?? new PostgresNodeRepository(createPool(config.databaseUrl)) },
      { provide: STORAGE_REPOSITORY, useExisting: NODE_REPOSITORY },
      AdminGuard, NodesService,
    ],
  })
  class AppModule {}

  const adapter = new FastifyAdapter({ bodyLimit: 16 * 1024, trustProxy: false });
  const app = await NestFactory.create<NestFastifyApplication>(AppModule, adapter, { logger: ['error', 'warn'] });
  adapter.getInstance().addHook('onSend', async (_request, reply, payload) => {
    reply.header('Cache-Control', 'no-store');
    reply.header('X-Content-Type-Options', 'nosniff');
    return payload;
  });
  app.enableShutdownHooks();
  await app.init();
  return app;
}
