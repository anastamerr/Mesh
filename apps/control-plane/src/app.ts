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
import { PostgresWorkloadRepository } from './database/postgres.workload-repository';
import { WORKLOAD_REPOSITORY, WorkloadRepository } from './workloads/repository';
import { WorkloadsController } from './workloads/workloads.controller';
import { WorkloadsService } from './workloads/workloads.service';

type AppRepository = NodeRepository & StorageRepository & PairingRepository & RelayRepository & WorkloadRepository;

export async function createApp(config: Config, repository?: AppRepository) {
  let nodeRepository: NodeRepository;
  let workloadRepository: WorkloadRepository;
  if (repository) {
    nodeRepository = repository;
    workloadRepository = repository;
  } else {
    const pool = createPool(config.databaseUrl);
    nodeRepository = new PostgresNodeRepository(pool);
    workloadRepository = new PostgresWorkloadRepository(pool);
  }
  // An injected composite repository is one lifecycle owner. Alias its
  // workload token so Nest does not invoke shutdown hooks twice on one value.
  const workloadProvider = repository
    ? { provide: WORKLOAD_REPOSITORY, useExisting: NODE_REPOSITORY }
    : { provide: WORKLOAD_REPOSITORY, useValue: workloadRepository };
  @Module({
    controllers: [HealthController, NodesController, PairingController, RelayController, RelayTicketsController,
      NetworkController, StorageController, CollectionsController, WorkloadsController],
    providers: [
      { provide: CONFIG, useValue: config },
      { provide: NODE_REPOSITORY, useValue: nodeRepository },
      { provide: STORAGE_REPOSITORY, useExisting: NODE_REPOSITORY },
      { provide: PAIRING_REPOSITORY, useExisting: NODE_REPOSITORY },
      workloadProvider,
      AdminGuard, RelayGuard, NodesService, PairingService, WorkloadsService,
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
