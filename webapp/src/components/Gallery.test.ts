// @vitest-environment jsdom

import { createPinia, setActivePinia } from 'pinia';
import { config, shallowMount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import Gallery from './Gallery.vue';
import QueueTargetSelect from './QueueTargetSelect.vue';
import { useGalleryStore } from '../stores/gallery';
import { useQueueStore } from '../stores/queue';
import { useQueueTargetStore } from '../stores/queueTarget';

describe('Gallery queue target integration', () => {
  beforeEach(() => {
    sessionStorage.clear();
    setActivePinia(createPinia());
    config.global.renderStubDefaultSlot = true;
  });

  it('dispatches membership and enqueue operations to the shared selected target', async () => {
    const gallery = useGalleryStore();
    gallery.photos = [
      { id: 55, thumbnail_url: '/thumbnail/55', orientation: 'landscape' },
    ];
    gallery.totalPhotos = 1;
    vi.spyOn(gallery, 'fetchPhotos').mockResolvedValue();

    const target = useQueueTargetStore();
    target.devices = [
      {
        id: 7,
        name: 'Frame 7',
        host: 'frame-7',
        width: 800,
        height: 480,
        orientation: 'landscape',
        enable_collage: false,
        server_authoritative: true,
        config_sync_pending: false,
        created_at: '2026-01-01T00:00:00Z',
      },
    ];
    vi.spyOn(target, 'fetchDevices').mockResolvedValue();

    const queue = useQueueStore();
    const check = vi.spyOn(queue, 'checkQueued').mockResolvedValue([]);
    const add = vi.spyOn(queue, 'addToQueue').mockResolvedValue({
      added: [],
      rejected: [],
      warning: '',
    });

    const wrapper = shallowMount(Gallery);
    target.setTarget(7);
    await wrapper.vm.$nextTick();
    expect(wrapper.findComponent(QueueTargetSelect).exists()).toBe(true);
    expect(check).toHaveBeenCalledWith(7, [55]);

    await wrapper.find('.queue-overlay').trigger('click');
    expect(add).toHaveBeenCalledWith(7, [55]);
  });
});
