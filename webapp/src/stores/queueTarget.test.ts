// @vitest-environment jsdom

import { createPinia, setActivePinia } from 'pinia';
import { shallowMount } from '@vue/test-utils';
import { defineComponent, nextTick } from 'vue';
import { beforeEach, describe, expect, it } from 'vitest';
import type { Device } from '../api';
import QueueTargetSelect from '../components/QueueTargetSelect.vue';
import { QUEUE_TARGET_SESSION_KEY, useQueueTargetStore } from './queueTarget';

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

const VSelectStub = defineComponent({
  name: 'VSelect',
  props: ['modelValue', 'items'],
  emits: ['update:modelValue'],
  template: '<select></select>',
});

describe('shared queue target', () => {
  beforeEach(() => {
    sessionStorage.clear();
    setActivePinia(createPinia());
  });

  it('selects no target for zero devices', () => {
    const store = useQueueTargetStore();
    store.validateDevices([]);
    expect(store.targetDeviceId).toBeNull();
  });

  it('automatically selects the only device', () => {
    const store = useQueueTargetStore();
    store.validateDevices([device(7)]);
    expect(store.targetDeviceId).toBe(7);
    expect(sessionStorage.getItem(QUEUE_TARGET_SESSION_KEY)).toBe('7');
  });

  it('requires selection when multiple devices have no valid session target', () => {
    sessionStorage.setItem(QUEUE_TARGET_SESSION_KEY, '99');
    const store = useQueueTargetStore();
    store.validateDevices([device(1), device(2)]);
    expect(store.targetDeviceId).toBeNull();
    expect(sessionStorage.getItem(QUEUE_TARGET_SESSION_KEY)).toBeNull();
  });

  it('restores a valid target from this session', () => {
    sessionStorage.setItem(QUEUE_TARGET_SESSION_KEY, '2');
    const store = useQueueTargetStore();
    store.validateDevices([device(1), device(2)]);
    expect(store.targetDeviceId).toBe(2);
  });

  it('keeps Gallery and QueueTab selectors on the same target', async () => {
    const pinia = createPinia();
    setActivePinia(pinia);
    const options = {
      global: { plugins: [pinia], stubs: { VSelect: VSelectStub } },
    };
    const gallery = shallowMount(QueueTargetSelect, {
      ...options,
      props: { testId: 'gallery-queue-target' },
    });
    const queueTab = shallowMount(QueueTargetSelect, {
      ...options,
      props: { testId: 'queue-tab-target' },
    });
    const sharedStore = useQueueTargetStore();
    sharedStore.validateDevices([device(1), device(2)]);

    gallery.findComponent(VSelectStub).vm.$emit('update:modelValue', 2);
    await nextTick();
    expect(queueTab.findComponent(VSelectStub).props('modelValue')).toBe(2);

    queueTab.findComponent(VSelectStub).vm.$emit('update:modelValue', 1);
    await nextTick();
    expect(gallery.findComponent(VSelectStub).props('modelValue')).toBe(1);
    expect(sessionStorage.getItem(QUEUE_TARGET_SESSION_KEY)).toBe('1');
  });
});
