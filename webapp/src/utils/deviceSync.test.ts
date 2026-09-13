import { describe, expect, it } from 'vitest';
import {
  DEFAULT_SERVER_AUTHORITATIVE,
  canImportDeviceSettings,
  deviceSyncMessage,
} from './deviceSync';

describe('device authority UI state', () => {
  it('defaults new devices to server authority', () => {
    expect(DEFAULT_SERVER_AUTHORITATIVE).toBe(true);
  });

  it('disables only setting import in authoritative mode', () => {
    expect(canImportDeviceSettings(true)).toBe(false);
    expect(canImportDeviceSettings(false)).toBe(true);
  });

  it('uses pushed and pending language without acknowledgement claims', () => {
    expect(deviceSyncMessage(true, true)).toContain('pending');
    expect(deviceSyncMessage(true, false)).toContain('pushed');
    expect(deviceSyncMessage(false, false)).toContain('legacy');
    expect(deviceSyncMessage(true, false)).not.toContain('synchronized');
  });
});
