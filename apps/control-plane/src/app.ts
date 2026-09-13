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
import { RateLimiter } from './http/rate-limit';
import { Metrics, MetricsController } from './observability/metrics';

interface AppRepositories {
  nodes: NodeRepository & StorageRepository & PairingRepository & RelayRepository;
  workloads: WorkloadRepository;
}

export async function createApp(config: Config, repositories?: AppRepositories) {
  if (!repositories) {
    const pool = createPool(config.databaseUrl);
    repositories = { nodes: new PostgresNodeRepository(pool), workloads: new PostgresWorkloadRepository(pool) };
  }
  const { nodes, workloads } = repositories;
  // Alias shared test repositories so Nest invokes their shutdown hook only once.
  const workloadProvider = Object.is(nodes, workloads)
    ? { provide: WORKLOAD_REPOSITORY, useExisting: NODE_REPOSITORY }
    : { provide: WORKLOAD_REPOSITORY, useValue: workloads };
  @Module({
    controllers: [HealthController, MetricsController, NodesController, PairingController, RelayController, RelayTicketsController,
      NetworkController, StorageController, CollectionsController, WorkloadsController],
    providers: [
      { provide: CONFIG, useValue: config },
      { provide: NODE_REPOSITORY, useValue: nodes },
      { provide: STORAGE_REPOSITORY, useExisting: NODE_REPOSITORY },
      { provide: PAIRING_REPOSITORY, useExisting: NODE_REPOSITORY },
      workloadProvider,
      AdminGuard, RelayGuard, NodesService, PairingService, WorkloadsService, Metrics,
    ],
  })
  class AppModule {}

  const trustedHops = config.trustProxyHops ?? 0;
  const adapter = new FastifyAdapter({ bodyLimit: 16 * 1024,
    trustProxy: trustedHops > 0 ? (_address, hop) => hop < trustedHops : false });
  const app = await NestFactory.create<NestFastifyApplication>(AppModule, adapter, { logger: ['error', 'warn'] });
  const metrics = app.get(Metrics);
  const starts = new WeakMap<object, bigint>();
  const apiLimit = new RateLimiter(config.apiRateLimitPerMinute ?? 600);
  const pairingLimit = new RateLimiter(config.pairingRateLimitPerMinute ?? 10);
  adapter.getInstance().addHook('onRequest', async (request, reply) => {
    starts.set(request, process.hrtime.bigint());
    const path = request.url.split('?', 1)[0] ?? '';
    if (!path.startsWith('/v1/')) return;
    const pairing = request.method === 'POST' && path === '/v1/pairing-challenges';
    const decision = (pairing ? pairingLimit : apiLimit).check(`${pairing ? 'pairing' : 'api'}:${request.ip}`);
    if (!decision.allowed) {
      reply.header('Retry-After', String(decision.retryAfterSeconds));
      await reply.code(429).send({ statusCode: 429, message: 'Too Many Requests' });
    }
  });
  adapter.getInstance().addHook('onResponse', async (request, reply) => {
    const start = starts.get(request);
    const seconds = start ? Number(process.hrtime.bigint() - start) / 1e9 : 0;
    metrics.observe(request.method, request.routeOptions.url ?? 'unmatched', reply.statusCode, seconds);
  });
  adapter.getInstance().addHook('onSend', async (_request, reply, payload) => {
    reply.header('Cache-Control', 'no-store');
    reply.header('X-Content-Type-Options', 'nosniff');
    return payload;
  });
  app.enableShutdownHooks();
  await app.init();
  return app;
}
