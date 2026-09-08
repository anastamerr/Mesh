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
import { PairingController } from './pairing/pairing.controller';
import { PAIRING_REPOSITORY, PairingRepository } from './pairing/repository';
import { PairingService } from './pairing/pairing.service';
import { RelayController, RelayGuard, RelayRepository, RelayTicketsController } from './relay/relay.controller';
import { NetworkController } from './relay/network.controller';

export async function createApp(config: Config, repository?: NodeRepository & StorageRepository & PairingRepository & RelayRepository) {
  @Module({
    controllers: [HealthController, NodesController, PairingController, RelayController, RelayTicketsController, NetworkController, StorageController, CollectionsController],
    providers: [
      { provide: CONFIG, useValue: config },
      { provide: NODE_REPOSITORY, useFactory: () => repository ?? new PostgresNodeRepository(createPool(config.databaseUrl)) },
      { provide: STORAGE_REPOSITORY, useExisting: NODE_REPOSITORY },
      { provide: PAIRING_REPOSITORY, useExisting: NODE_REPOSITORY },
      AdminGuard, RelayGuard, NodesService, PairingService,
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
