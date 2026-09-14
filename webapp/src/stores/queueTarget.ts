import { defineStore } from 'pinia';
import { ref } from 'vue';
import { listDevices, type Device } from '../api';

export const QUEUE_TARGET_SESSION_KEY = 'photoframe_queue_target_device';

function storedTarget(): number | null {
  const value = sessionStorage.getItem(QUEUE_TARGET_SESSION_KEY);
  if (value === null) return null;
  const id = Number(value);
  return Number.isInteger(id) && id > 0 ? id : null;
}

export const useQueueTargetStore = defineStore('queueTarget', () => {
  const devices = ref<Device[]>([]);
  const targetDeviceId = ref<number | null>(null);
  const loading = ref(false);
  let savedTarget = storedTarget();
  let requestGeneration = 0;

  function setTarget(id: number | null) {
    const validId = devices.value.some((device) => device.id === id)
      ? id
      : null;
    targetDeviceId.value = validId;
    savedTarget = validId;
    if (validId === null) {
      sessionStorage.removeItem(QUEUE_TARGET_SESSION_KEY);
    } else {
      sessionStorage.setItem(QUEUE_TARGET_SESSION_KEY, String(validId));
    }
  }

  function validateDevices(list: Device[]) {
    devices.value = list;
    const current = targetDeviceId.value ?? savedTarget;
    if (current !== null && list.some((device) => device.id === current)) {
      setTarget(current);
    } else if (list.length === 1) {
      setTarget(list[0].id);
    } else {
      setTarget(null);
    }
  }

  async function fetchDevices() {
    const generation = ++requestGeneration;
    loading.value = true;
    try {
      const list = await listDevices();
      if (generation === requestGeneration) validateDevices(list);
    } finally {
      if (generation === requestGeneration) loading.value = false;
    }
  }

  return {
    devices,
    targetDeviceId,
    loading,
    setTarget,
    validateDevices,
    fetchDevices,
  };
});
