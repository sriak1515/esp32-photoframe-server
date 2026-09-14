import { defineStore } from 'pinia';
import { api } from '../api';

export type QueueState =
  | 'pending'
  | 'claimed'
  | 'leased'
  | 'failed'
  | 'permanently_invalid'
  | 'delivered';

export interface QueueItem {
  id: number;
  device_id: number;
  image_id: number;
  position: number;
  source: string;
  created_at: string;
  state: QueueState;
  claim_expires_at?: string;
  lease_expires_at?: string;
  attempt_count: number;
  next_attempt_at?: string;
  last_attempt_at?: string;
  last_error_code?: string;
  last_error?: string;
  cache_status?: string;
  image?: {
    id: number;
    caption: string;
    orientation: string;
    thumbnail_url: string;
  };
}

export interface QueueStatus {
  count: number;
  soft_limit: number;
  next_image_id: number | null;
  next_item: QueueItem | null;
  state_counts: Record<string, number>;
}

interface DeviceQueue {
  items: QueueItem[];
  count: number;
  softLimit: number;
  loading: boolean;
  status: QueueStatus | null;
}

const EMPTY_QUEUE: DeviceQueue = {
  items: [],
  count: 0,
  softLimit: 500,
  loading: false,
  status: null,
};

function isConflict(error: unknown): boolean {
  return (
    typeof error === 'object' &&
    error !== null &&
    'response' in error &&
    (error as { response?: { status?: number } }).response?.status === 409
  );
}

export function isQueueItemLocked(item: QueueItem): boolean {
  return item.state === 'claimed' || item.state === 'leased';
}

export const useQueueStore = defineStore('queue', {
  state: () => ({
    byDevice: {} as Record<number, DeviceQueue>,
    queuedImageIdsByDevice: {} as Record<number, number[]>,
    queueGenerations: {} as Record<number, number>,
    membershipGenerations: {} as Record<number, number>,
    statusGenerations: {} as Record<number, number>,
  }),

  getters: {
    queueFor: (state) => (deviceId: number | null) =>
      deviceId === null ? EMPTY_QUEUE : state.byDevice[deviceId] || EMPTY_QUEUE,
    queuedImageIdsFor: (state) => (deviceId: number | null) =>
      deviceId === null ? [] : state.queuedImageIdsByDevice[deviceId] || [],
  },

  actions: {
    ensureDevice(deviceId: number): DeviceQueue {
      if (!this.byDevice[deviceId]) {
        this.byDevice[deviceId] = {
          items: [],
          count: 0,
          softLimit: 500,
          loading: false,
          status: null,
        };
      }
      return this.byDevice[deviceId];
    },

    setItems(deviceId: number, items: QueueItem[]) {
      this.ensureDevice(deviceId).items = items;
    },

    async fetchQueue(deviceId: number) {
      const generation = (this.queueGenerations[deviceId] || 0) + 1;
      const membershipGeneration =
        (this.membershipGenerations[deviceId] || 0) + 1;
      this.queueGenerations[deviceId] = generation;
      this.membershipGenerations[deviceId] = membershipGeneration;
      this.ensureDevice(deviceId).loading = true;
      try {
        const res = await api.get(`/devices/${deviceId}/queue`);
        if (this.queueGenerations[deviceId] !== generation) return;
        const queue = this.ensureDevice(deviceId);
        queue.items = res.data.items || [];
        queue.count = res.data.count || 0;
        queue.softLimit = res.data.soft_limit || 500;
        if (this.membershipGenerations[deviceId] === membershipGeneration) {
          this.queuedImageIdsByDevice[deviceId] = [
            ...new Set<number>(queue.items.map((item) => item.image_id)),
          ];
        }
      } catch (error) {
        console.error('Failed to fetch queue', error);
        throw error;
      } finally {
        if (this.queueGenerations[deviceId] === generation) {
          this.ensureDevice(deviceId).loading = false;
        }
      }
    },

    async fetchStatus(deviceId: number) {
      const generation = (this.statusGenerations[deviceId] || 0) + 1;
      this.statusGenerations[deviceId] = generation;
      try {
        const res = await api.get(`/devices/${deviceId}/queue/status`);
        if (this.statusGenerations[deviceId] === generation) {
          this.ensureDevice(deviceId).status = res.data;
        }
      } catch (error) {
        console.error('Failed to fetch queue status', error);
        throw error;
      }
    },

    async refreshQueue(deviceId: number) {
      await Promise.all([
        this.fetchQueue(deviceId),
        this.fetchStatus(deviceId),
      ]);
    },

    async addToQueue(deviceId: number, imageIds: number[]) {
      try {
        const res = await api.post(`/devices/${deviceId}/queue`, {
          image_ids: imageIds,
        });
        await this.refreshQueue(deviceId);
        return res.data;
      } catch (error) {
        console.error('Failed to add to queue', error);
        throw error;
      }
    },

    async recoverConflict(deviceId: number, error: unknown) {
      if (isConflict(error)) await this.refreshQueue(deviceId);
    },

    async removeItem(deviceId: number, itemId: number) {
      try {
        await api.delete(`/devices/${deviceId}/queue/${itemId}`);
        await this.refreshQueue(deviceId);
      } catch (error) {
        await this.recoverConflict(deviceId, error);
        console.error('Failed to remove from queue', error);
        throw error;
      }
    },

    async clearQueue(deviceId: number) {
      try {
        await api.delete(`/devices/${deviceId}/queue`);
        await this.refreshQueue(deviceId);
      } catch (error) {
        await this.recoverConflict(deviceId, error);
        console.error('Failed to clear queue', error);
        throw error;
      }
    },

    async reorderQueue(deviceId: number, itemIds: number[]) {
      try {
        await api.put(`/devices/${deviceId}/queue/reorder`, {
          item_ids: itemIds,
        });
        await this.refreshQueue(deviceId);
      } catch (error) {
        await this.recoverConflict(deviceId, error);
        console.error('Failed to reorder queue', error);
        throw error;
      }
    },

    async retryInvalid(deviceId: number, itemId: number) {
      await api.post(`/devices/${deviceId}/queue/${itemId}/retry`);
      await this.refreshQueue(deviceId);
    },

    async checkQueued(deviceId: number, imageIds: number[]): Promise<number[]> {
      const generation = (this.membershipGenerations[deviceId] || 0) + 1;
      this.membershipGenerations[deviceId] = generation;
      try {
        const res = await api.post(`/devices/${deviceId}/queue/check`, {
          image_ids: imageIds,
        });
        const queued = res.data.queued || [];
        if (this.membershipGenerations[deviceId] === generation) {
          this.queuedImageIdsByDevice[deviceId] = queued;
        }
        return queued;
      } catch (error) {
        console.error('Failed to check queued status', error);
        return [];
      }
    },
  },
});
