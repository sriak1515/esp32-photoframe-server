import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../api';
import { useQueueStore, type QueueItem } from './queue';

function item(id: number, imageId = id): QueueItem {
  return {
    id,
    device_id: 1,
    image_id: imageId,
    position: id * 10,
    source: 'gallery',
    created_at: '2026-01-01T00:00:00Z',
    state: 'pending',
    attempt_count: 0,
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

describe('device-keyed queue state', () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    vi.restoreAllMocks();
    vi.spyOn(console, 'error').mockImplementation(() => undefined);
  });

  it('does not let an older queue response replace a newer one', async () => {
    const first = deferred<{ data: { items: QueueItem[]; count: number } }>();
    const second = deferred<{ data: { items: QueueItem[]; count: number } }>();
    vi.spyOn(api, 'get')
      .mockImplementationOnce(() => first.promise)
      .mockImplementationOnce(() => second.promise);
    const store = useQueueStore();

    const olderRequest = store.fetchQueue(1);
    const newerRequest = store.fetchQueue(1);
    second.resolve({ data: { items: [item(2)], count: 1 } });
    await newerRequest;
    first.resolve({ data: { items: [item(1)], count: 1 } });
    await olderRequest;

    expect(store.queueFor(1).items.map((entry) => entry.id)).toEqual([2]);
  });

  it('keeps late membership responses scoped to their device and generation', async () => {
    const oldCheck = deferred<{ data: { queued: number[] } }>();
    const newCheck = deferred<{ data: { queued: number[] } }>();
    vi.spyOn(api, 'post')
      .mockImplementationOnce(() => oldCheck.promise)
      .mockImplementationOnce(() => newCheck.promise);
    const store = useQueueStore();

    const olderRequest = store.checkQueued(1, [10]);
    const newerRequest = store.checkQueued(1, [20]);
    newCheck.resolve({ data: { queued: [20] } });
    await newerRequest;
    oldCheck.resolve({ data: { queued: [10] } });
    await olderRequest;

    expect(store.queuedImageIdsFor(1)).toEqual([20]);
    expect(store.queuedImageIdsFor(2)).toEqual([]);
  });

  it('uses the same captured target for status and enqueue', async () => {
    const get = vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        items: [],
        count: 0,
        soft_limit: 500,
        next_image_id: null,
        next_item: null,
        state_counts: {},
      },
    });
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      data: { added: [item(1, 42)], rejected: [] },
    });
    const store = useQueueStore();
    const target = 8;

    await store.fetchStatus(target);
    await store.addToQueue(target, [42]);

    expect(get).toHaveBeenCalledWith('/devices/8/queue/status');
    expect(post).toHaveBeenCalledWith('/devices/8/queue', { image_ids: [42] });
    expect(get).toHaveBeenCalledWith('/devices/8/queue');
    expect(get).toHaveBeenCalledWith('/devices/8/queue/status');
  });

  it('refetches the affected queue after a 409 conflict', async () => {
    vi.spyOn(api, 'delete').mockRejectedValue({ response: { status: 409 } });
    const get = vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        items: [item(4)],
        count: 1,
        soft_limit: 500,
        next_image_id: 4,
        next_item: item(4),
        state_counts: { pending: 1 },
      },
    });
    const store = useQueueStore();

    await expect(store.removeItem(3, 4)).rejects.toMatchObject({
      response: { status: 409 },
    });
    expect(get).toHaveBeenCalledWith('/devices/3/queue');
    expect(get).toHaveBeenCalledWith('/devices/3/queue/status');
  });
});
