import type { CollectionRegistration, CollectionStatistics } from './collection.contracts';
import { StoragePermission } from './contracts';

export const STORAGE_REPOSITORY = Symbol('STORAGE_REPOSITORY');
export interface CollectionRecord extends CollectionRegistration { confirmedAt: Date | null; }
export interface StorageRepository {
  registerCollection(nodeId: string, input: CollectionRegistration): Promise<boolean>;
  confirmCollection(nodeId: string, nodeHash: string, input: CollectionStatistics): Promise<boolean>;
  listCollections(nodeId: string, after: string): Promise<CollectionRecord[]>;
  createStorageGrant(nodeId: string, hash: string, permission: StoragePermission, expiresAt: Date): Promise<boolean>;
  validateStorageGrant(nodeId: string, nodeHash: string, grantHash: string, permission: StoragePermission): Promise<boolean>;
}
