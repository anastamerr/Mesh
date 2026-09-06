import { StoragePermission } from './contracts';

export const STORAGE_GRANTS = Symbol('STORAGE_GRANTS');
export interface StorageGrantRepository {
  createStorageGrant(nodeId: string, hash: string, permission: StoragePermission, expiresAt: Date): Promise<boolean>;
  validateStorageGrant(nodeId: string, nodeHash: string, grantHash: string, permission: StoragePermission): Promise<boolean>;
}
