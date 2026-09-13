import { z } from 'zod';

const digestImageSchema = z.string().min(73).max(512)
  .regex(/^[A-Za-z0-9][A-Za-z0-9._/:_-]*@sha256:[a-f0-9]{64}$/);
const commandSchema = z.array(z.string().min(1).max(4096)).min(1).max(64);
const collectionSchema = z.string().regex(/^[a-f0-9]{64}$/);

const commonWorkloadSchema = z.object({
  id: z.uuid({ version: 'v4' }),
  nodeId: z.uuid({ version: 'v4' }),
  name: z.string().trim().min(1).max(100),
  image: digestImageSchema,
  command: commandSchema,
  resources: z.object({
    cpuMillis: z.number().int().min(100).max(64_000),
    memoryBytes: z.number().int().min(64 * 1024 * 1024).max(Number.MAX_SAFE_INTEGER),
  }).strict(),
  inputCollectionId: collectionSchema.nullable(),
}).strict();

const jobSchema = commonWorkloadSchema.extend({
  kind: z.literal('job'),
  desiredState: z.literal('running'),
  servicePort: z.null(),
}).strict();

const applicationSchema = commonWorkloadSchema.extend({
  kind: z.literal('application'),
  desiredState: z.enum(['running', 'stopped']),
  servicePort: z.number().int().min(1).max(65_535),
}).strict();

export const createWorkloadSchema = z.discriminatedUnion('kind', [jobSchema, applicationSchema]);
export const desiredStateSchema = z.object({ desiredState: z.enum(['running', 'stopped']) }).strict();
export const workloadObservationSchema = z.object({
  revision: z.number().int().positive().max(Number.MAX_SAFE_INTEGER),
  state: z.enum(['pulling', 'starting', 'running', 'exporting', 'succeeded', 'failed', 'stopped']),
  exitCode: z.number().int().min(0).max(255).nullable(),
  failureCode: z.enum(['image-unavailable', 'invalid-runtime', 'resource-unavailable', 'runtime-failure']).nullable(),
  outputCollectionId: collectionSchema.nullable(),
}).strict().superRefine((value, context) => {
  if ((value.state === 'succeeded' || value.state === 'failed') !== (value.exitCode !== null)) {
    context.addIssue({ code: 'custom', message: 'Exit code is required only for terminal execution states', path: ['exitCode'] });
  }
  if ((value.state === 'failed') !== (value.failureCode !== null)) {
    context.addIssue({ code: 'custom', message: 'Failure code is required only for failed executions', path: ['failureCode'] });
  }
  if ((value.state === 'succeeded') !== (value.outputCollectionId !== null)) {
    context.addIssue({ code: 'custom', message: 'A successful job requires its verified output collection', path: ['outputCollectionId'] });
  }
});

export const executionEnvironmentSchema = z.object({
  kind: z.literal('docker-linux'),
  architecture: z.enum(['amd64', 'arm64']),
  status: z.enum(['ready', 'unavailable']),
  runtimeVersion: z.string().min(1).max(40).regex(/^[A-Za-z0-9.+-]+$/).nullable(),
}).strict().superRefine((value, context) => {
  if ((value.status === 'ready') !== (value.runtimeVersion !== null)) {
    context.addIssue({ code: 'custom', message: 'A ready environment requires a runtime version', path: ['runtimeVersion'] });
  }
});

export type CreateWorkload = z.infer<typeof createWorkloadSchema>;
export type DesiredState = z.infer<typeof desiredStateSchema>['desiredState'];
export type WorkloadObservation = z.infer<typeof workloadObservationSchema>;
export type ExecutionEnvironmentReport = z.infer<typeof executionEnvironmentSchema>;
