<template>
  <v-card-text>
    <div class="d-flex align-center mb-4">
      <v-select
        v-model="selectedDevice"
        :items="devices"
        item-title="name"
        item-value="id"
        label="Device"
        variant="outlined"
        density="compact"
        hide-details
        class="mr-4"
        style="max-width: 300px"
        @update:model-value="onDeviceChange"
      ></v-select>
      <v-spacer></v-spacer>
      <v-chip
        :color="queueCount >= softLimit ? 'warning' : 'primary'"
        variant="tonal"
        class="mr-4"
      >
        {{ queueCount }} / {{ softLimit }}
      </v-chip>
      <v-btn
        color="error"
        variant="outlined"
        :disabled="queueCount === 0"
        @click="confirmClear"
      >
        Clear Queue
      </v-btn>
    </div>

    <div v-if="loading" class="text-center py-8">
      <v-progress-circular indeterminate color="primary"></v-progress-circular>
    </div>

    <div v-else-if="items.length === 0" class="text-center py-8 text-grey">
      No images in queue. Add images from the gallery tabs below.
    </div>

    <draggable
      v-else
      v-model="items"
      item-key="id"
      class="queue-grid"
      ghost-class="queue-ghost"
      drag-class="queue-drag"
      handle=".queue-item"
      @end="onDragEnd"
    >
      <template #item="{ element }">
        <div class="queue-grid-item">
          <v-card class="queue-item" variant="outlined">
            <v-img
              :src="
                getThumbnailUrl(
                  element.image?.thumbnail_url || '/placeholder.png'
                )
              "
              :aspect-ratio="4 / 3"
              cover
              class="bg-grey-lighten-3"
            >
              <v-btn
                icon="mdi-close"
                size="x-small"
                color="error"
                variant="elevated"
                class="remove-btn"
                @click="removeItem(element.id)"
              ></v-btn>
            </v-img>
            <v-card-text class="pa-2">
              <div class="text-caption text-truncate">
                {{ element.image?.caption || `Image #${element.image_id}` }}
              </div>
              <div class="text-caption text-grey">
                {{ element.source }}
              </div>
            </v-card-text>
          </v-card>
        </div>
      </template>
    </draggable>

    <!-- Clear Confirmation Dialog -->
    <v-dialog v-model="showClearDialog" max-width="400">
      <v-card>
        <v-card-title>Clear Queue?</v-card-title>
        <v-card-text>
          This will remove all {{ queueCount }} images from the queue. This
          action cannot be undone.
        </v-card-text>
        <v-card-actions>
          <v-spacer></v-spacer>
          <v-btn color="grey" variant="text" @click="showClearDialog = false">
            Cancel
          </v-btn>
          <v-btn color="error" @click="clearQueue">Clear</v-btn>
        </v-card-actions>
      </v-card>
    </v-dialog>
  </v-card-text>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue';
import draggable from 'vuedraggable';
import { useQueueStore, type QueueItem } from '../stores/queue';
import { useAuthStore } from '../stores/auth';
import { useSnackbar } from '../composables/useSnackbar';
import { useDefaultDevice } from '../composables/useDefaultDevice';
import { listDevices, type Device } from '../api';

const queueStore = useQueueStore();
const authStore = useAuthStore();
const { showMessage } = useSnackbar();
const { activeQueueDeviceId } = useDefaultDevice();

const selectedDevice = ref<number | null>(null);
const showClearDialog = ref(false);
const devices = ref<Device[]>([]);

const items = computed<QueueItem[]>({
  get: () => queueStore.items,
  set: (val: QueueItem[]) => {
    queueStore.items = val;
  },
});
const queueCount = computed(() => queueStore.count);
const softLimit = computed(() => queueStore.softLimit);
const loading = computed(() => queueStore.loading);

onMounted(async () => {
  try {
    devices.value = await listDevices();
    if (devices.value.length > 0 && !selectedDevice.value) {
      selectedDevice.value = devices.value[0].id;
      activeQueueDeviceId.value = selectedDevice.value;
      onDeviceChange();
    }
  } catch (e) {
    console.error('Failed to fetch devices', e);
  }
});

function onDeviceChange() {
  if (selectedDevice.value) {
    activeQueueDeviceId.value = selectedDevice.value;
    queueStore.fetchQueue(selectedDevice.value);
  }
}

function getThumbnailUrl(url: string) {
  const token = authStore.token;
  if (!token) return url;
  const separator = url.includes('?') ? '&' : '?';
  return `${url}${separator}token=${token}`;
}

async function removeItem(itemId: number) {
  if (!selectedDevice.value) return;
  try {
    await queueStore.removeItem(selectedDevice.value, itemId);
    showMessage('Removed from queue');
  } catch {
    showMessage('Failed to remove from queue', true);
  }
}

function confirmClear() {
  showClearDialog.value = true;
}

async function clearQueue() {
  if (!selectedDevice.value) return;
  try {
    await queueStore.clearQueue(selectedDevice.value);
    showClearDialog.value = false;
    showMessage('Queue cleared');
  } catch {
    showMessage('Failed to clear queue', true);
  }
}

async function onDragEnd() {
  if (!selectedDevice.value) return;
  const itemIds = items.value.map((item) => item.id);
  try {
    await queueStore.reorderQueue(selectedDevice.value, itemIds);
  } catch {
    showMessage('Failed to reorder queue', true);
    await queueStore.fetchQueue(selectedDevice.value);
  }
}
</script>

<style scoped>
.queue-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 12px;
}

@media (min-width: 600px) {
  .queue-grid {
    grid-template-columns: repeat(3, 1fr);
  }
}

@media (min-width: 960px) {
  .queue-grid {
    grid-template-columns: repeat(4, 1fr);
  }
}

@media (min-width: 1280px) {
  .queue-grid {
    grid-template-columns: repeat(6, 1fr);
  }
}

.queue-grid-item {
  min-width: 0;
}

.queue-item {
  position: relative;
  cursor: grab;
}

.queue-item:active {
  cursor: grabbing;
}

.remove-btn {
  position: absolute;
  top: 8px;
  right: 8px;
  z-index: 1;
}

:deep(.queue-ghost) {
  opacity: 0.4;
}

:deep(.queue-drag) {
  opacity: 0.8;
  box-shadow: 0 8px 16px rgba(0, 0, 0, 0.3);
  transform: rotate(2deg);
}
</style>
