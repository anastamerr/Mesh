import { z } from 'zod';

export const statisticsSchema = z.object({
  id: z.string().regex(/^[a-f0-9]{64}$/),
  fileCount: z.number().int().min(0).max(10000),
  totalBytes: z.number().int().min(0).max(Number.MAX_SAFE_INTEGER),
}).strict();
export const registrationSchema = statisticsSchema.extend({ name: z.string().trim().min(1).max(100).refine(name => [...name].every(character => character.charCodeAt(0) >= 32 && character.charCodeAt(0) !== 127)) });
export type CollectionStatistics = z.infer<typeof statisticsSchema>;
export type CollectionRegistration = z.infer<typeof registrationSchema>;

