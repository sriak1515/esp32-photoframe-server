import { defineStore } from 'pinia';
import { api } from '../api';

export interface QueueItem {
  id: number;
  device_id: number;
  image_id: number;
  position: number;
  source: string;
  created_at: string;
  image?: {
    id: number;
    caption: string;
    orientation: string;
    thumbnail_url: string;
  };
}

export const useQueueStore = defineStore('queue', {
  state: () => ({
    deviceId: null as number | null,
    items: [] as QueueItem[],
    count: 0,
    softLimit: 500,
    loading: false,
  }),

  actions: {
    async fetchQueue(deviceId: number) {
      this.deviceId = deviceId;
      this.loading = true;
      try {
        const res = await api.get(`/devices/${deviceId}/queue`);
        this.items = res.data.items || [];
        this.count = res.data.count || 0;
        this.softLimit = res.data.soft_limit || 500;
      } catch (e) {
        console.error('Failed to fetch queue', e);
      } finally {
        this.loading = false;
      }
    },

    async addToQueue(deviceId: number, imageIds: number[]) {
      try {
        const res = await api.post(`/devices/${deviceId}/queue`, {
          image_ids: imageIds,
        });
        // Refetch queue to get updated items
        await this.fetchQueue(deviceId);
        return res.data;
      } catch (e) {
        console.error('Failed to add to queue', e);
        throw e;
      }
    },

    async removeItem(deviceId: number, itemId: number) {
      try {
        await api.delete(`/devices/${deviceId}/queue/${itemId}`);
        // Optimistic update
        this.items = this.items.filter((item) => item.id !== itemId);
        this.count = Math.max(0, this.count - 1);
      } catch (e) {
        console.error('Failed to remove from queue', e);
        throw e;
      }
    },

    async clearQueue(deviceId: number) {
      try {
        await api.delete(`/devices/${deviceId}/queue`);
        this.items = [];
        this.count = 0;
      } catch (e) {
        console.error('Failed to clear queue', e);
        throw e;
      }
    },

    async reorderQueue(deviceId: number, itemIds: number[]) {
      try {
        await api.put(`/devices/${deviceId}/queue/reorder`, {
          item_ids: itemIds,
        });
        // Refetch to get updated positions
        await this.fetchQueue(deviceId);
      } catch (e) {
        console.error('Failed to reorder queue', e);
        throw e;
      }
    },

    async checkQueued(deviceId: number, imageIds: number[]): Promise<number[]> {
      try {
        const res = await api.post(`/devices/${deviceId}/queue/check`, {
          image_ids: imageIds,
        });
        return res.data.queued || [];
      } catch (e) {
        console.error('Failed to check queued status', e);
        return [];
      }
    },
  },
});
