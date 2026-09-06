import { BadRequestException } from '@nestjs/common';
import type { FastifyRequest } from 'fastify';
import { z } from 'zod';

export function parseBody<T>(schema: z.ZodType<T>, request: FastifyRequest): T {
  const result = schema.safeParse(request.body);
  if (!result.success) throw new BadRequestException({ message: 'Invalid request',
    issues: result.error.issues.map(i => ({ path: i.path.join('.'), message: i.message })) });
  return result.data;
}
