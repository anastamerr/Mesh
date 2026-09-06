import { z } from 'zod';

const collectionId = z.string().regex(/^[a-f0-9]{64}$/);
export const permissionSchema = z.discriminatedUnion('access', [
  z.object({ access: z.literal('read'), collectionId }).strict(),
  z.object({ access: z.literal('write'), collectionId }).strict(),
  z.object({ access: z.literal('list'), collectionId: z.null() }).strict(),
]);
export type StoragePermission = z.infer<typeof permissionSchema>;
export const validationSchema = z.object({
  token: z.string().regex(/^[A-Za-z0-9_-]{43}$/),
  permission: permissionSchema,
}).strict();
