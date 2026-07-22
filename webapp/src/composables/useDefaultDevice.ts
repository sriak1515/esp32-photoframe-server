import { ref } from 'vue';

// Session-only device preference for add-to-queue
const defaultDeviceId = ref<number | null>(null);

// Active queue device for display (QueueTab sets this, Gallery reads it)
const activeQueueDeviceId = ref<number | null>(null);

export function useDefaultDevice() {
  function setDefaultDevice(id: number) {
    defaultDeviceId.value = id;
  }

  function clearDefaultDevice() {
    defaultDeviceId.value = null;
  }

  return {
    defaultDeviceId,
    setDefaultDevice,
    clearDefaultDevice,
    activeQueueDeviceId,
  };
}
