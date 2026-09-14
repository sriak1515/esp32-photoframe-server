import { describe, expect, it } from 'vitest';
import { isQueueItemLocked, type QueueItem } from '../stores/queue';
import { queueStatePresentation } from './queuePresentation';

function item(state: QueueItem['state']): QueueItem {
  return {
    id: 1,
    device_id: 1,
    image_id: 1,
    position: 10,
    source: 'gallery',
    created_at: '2026-01-01T00:00:00Z',
    state,
    attempt_count: 0,
  };
}

describe('queue lifecycle presentation', () => {
  it.each([
    ['pending', 'Pending'],
    ['failed', 'Retry scheduled'],
    ['leased', 'Leased'],
    ['permanently_invalid', 'Invalid'],
  ] as const)('presents %s as %s', (state, label) => {
    expect(queueStatePresentation(item(state)).label).toBe(label);
  });

  it('locks only claimed and leased occurrences', () => {
    expect(isQueueItemLocked(item('claimed'))).toBe(true);
    expect(isQueueItemLocked(item('leased'))).toBe(true);
    expect(isQueueItemLocked(item('pending'))).toBe(false);
    expect(isQueueItemLocked(item('failed'))).toBe(false);
    expect(isQueueItemLocked(item('permanently_invalid'))).toBe(false);
  });
});
