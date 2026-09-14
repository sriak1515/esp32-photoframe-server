// @vitest-environment jsdom

import { createPinia, setActivePinia } from 'pinia';
import { config, shallowMount } from '@vue/test-utils';
import { defineComponent } from 'vue';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import QueueTab from './QueueTab.vue';
import { useQueueStore, type QueueItem } from '../stores/queue';
import { useQueueTargetStore } from '../stores/queueTarget';
import type { Device } from '../api';

function device(id: number): Device {
  return {
    id,
    name: `Frame ${id}`,
    host: `frame-${id}`,
    width: 800,
    height: 480,
    orientation: 'landscape',
    enable_collage: false,
    server_authoritative: true,
    config_sync_pending: false,
    created_at: '2026-01-01T00:00:00Z',
  };
}

function queueItem(id: number, state: QueueItem['state']): QueueItem {
  return {
    id,
    device_id: 1,
    image_id: 55,
    position: id * 10,
    source: 'immich',
    created_at: '2026-01-01T00:00:00Z',
    state,
    attempt_count: state === 'failed' ? 3 : 0,
    claim_expires_at: state === 'claimed' ? '2026-09-14T10:05:00Z' : undefined,
    lease_expires_at: state === 'leased' ? '2026-09-14T10:07:00Z' : undefined,
    next_attempt_at: state === 'failed' ? '2026-09-14T10:10:00Z' : undefined,
    last_error: state.includes('invalid')
      ? 'Image relation is missing'
      : undefined,
    last_error_code: state.includes('invalid') ? 'missing_image' : undefined,
    image: {
      id: 55,
      caption: 'Repeated photo',
      orientation: 'landscape',
      thumbnail_url: '/thumb/55',
    },
  };
}

const DraggableStub = defineComponent({
  name: 'Draggable',
  props: ['modelValue'],
  template:
    '<div><div v-for="entry in modelValue" :key="entry.id"><slot name="item" :element="entry" /></div></div>',
});

describe('QueueTab occurrence lifecycle', () => {
  beforeEach(() => {
    sessionStorage.clear();
    setActivePinia(createPinia());
    config.global.renderStubDefaultSlot = true;
  });

  it('renders duplicate occurrences separately with lifecycle metadata', () => {
    const target = useQueueTargetStore();
    target.validateDevices([device(1)]);
    vi.spyOn(target, 'fetchDevices').mockResolvedValue();
    const queue = useQueueStore();
    vi.spyOn(queue, 'refreshQueue').mockResolvedValue();
    queue.ensureDevice(1).items = [
      queueItem(101, 'pending'),
      queueItem(102, 'failed'),
      queueItem(103, 'leased'),
      queueItem(104, 'permanently_invalid'),
    ];
    queue.ensureDevice(1).count = 4;

    const wrapper = shallowMount(QueueTab, {
      global: { stubs: { draggable: DraggableStub, QueueTargetSelect: true } },
    });

    const cards = wrapper.findAll('[data-queue-item-id]');
    expect(cards).toHaveLength(4);
    expect(cards.map((card) => card.attributes('data-queue-item-id'))).toEqual([
      '101',
      '102',
      '103',
      '104',
    ]);
    expect(wrapper.text()).toContain('Pending');
    expect(wrapper.text()).toContain('Retry scheduled');
    expect(wrapper.text()).toContain('Attempt 3');
    expect(wrapper.text()).toContain('Best-effort transfer lease');
    expect(wrapper.text()).toContain('Invalid');
    expect(wrapper.text()).toContain('Image relation is missing');
    expect(
      wrapper.find('[aria-label="Remove queue occurrence 103"]').attributes()
    ).toHaveProperty('disabled');
  });
});
