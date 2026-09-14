<template>
  <v-card-text>
    <div
      class="d-flex flex-column flex-sm-row align-stretch align-sm-center ga-3 mb-4"
    >
      <QueueTargetSelect class="device-select" test-id="queue-tab-target" />
      <v-spacer></v-spacer>
      <div class="d-flex align-center justify-space-between ga-3">
        <v-chip
          :color="queueCount >= softLimit ? 'warning' : 'primary'"
          variant="tonal"
        >
          {{ queueCount }} / {{ softLimit }}
        </v-chip>
        <v-btn
          color="error"
          variant="outlined"
          :disabled="clearDisabled"
          @click="confirmClear"
        >
          Clear Queue
        </v-btn>
      </div>
    </div>

    <v-alert
      v-if="selectedDevice === null"
      type="info"
      variant="tonal"
      density="compact"
    >
      Select a queue target to view and manage its images.
    </v-alert>

    <div v-else-if="queue.loading" class="text-center py-8">
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
      handle=".queue-drag-handle"
      :move="canMove"
      @end="onDragEnd"
    >
      <template #item="{ element }">
        <div class="queue-grid-item" :data-queue-item-id="element.id">
          <v-card
            class="queue-item"
            :class="{ 'queue-item-locked': isQueueItemLocked(element) }"
            variant="outlined"
          >
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
                :disabled="isQueueItemLocked(element)"
                :aria-label="`Remove queue occurrence ${element.id}`"
                @click="removeItem(element.id)"
              ></v-btn>
            </v-img>
            <v-card-text class="pa-2">
              <div class="d-flex align-center ga-1 mb-1">
                <v-icon
                  v-if="!isQueueItemLocked(element)"
                  icon="mdi-drag"
                  size="small"
                  class="queue-drag-handle"
                ></v-icon>
                <v-chip
                  :color="queueStatePresentation(element).color"
                  :prepend-icon="queueStatePresentation(element).icon"
                  size="x-small"
                  variant="tonal"
                >
                  {{ queueStatePresentation(element).label }}
                </v-chip>
                <span class="text-caption text-grey ml-auto">
                  #{{ element.id }}
                </span>
              </div>
              <div class="text-caption text-truncate">
                {{ element.image?.caption || `Image #${element.image_id}` }}
              </div>
              <div class="text-caption text-grey">
                {{ element.source }}
                <span v-if="element.cache_status">
                  · cache {{ element.cache_status }}
                </span>
              </div>
              <div
                v-if="element.state === 'pending'"
                class="text-caption text-grey-darken-1 mt-1"
              >
                Waiting for a device poll
              </div>
              <div
                v-else-if="element.state === 'claimed'"
                class="text-caption text-warning-darken-2 mt-1"
              >
                Preparing for transfer
                <span v-if="element.claim_expires_at">
                  until {{ formatQueueTime(element.claim_expires_at) }}
                </span>
              </div>
              <div
                v-else-if="element.state === 'leased'"
                class="text-caption text-info-darken-2 mt-1"
              >
                Best-effort transfer lease
                <span v-if="element.lease_expires_at">
                  until {{ formatQueueTime(element.lease_expires_at) }}
                </span>
              </div>
              <div
                v-else-if="element.state === 'failed'"
                class="text-caption text-warning-darken-2 mt-1"
              >
                Attempt {{ element.attempt_count }}
                <span v-if="element.next_attempt_at">
                  · retry after {{ formatQueueTime(element.next_attempt_at) }}
                </span>
              </div>
              <div
                v-else-if="element.state === 'permanently_invalid'"
                class="text-caption text-error mt-1"
              >
                {{
                  element.last_error || 'This occurrence cannot be delivered'
                }}
                <span v-if="element.last_error_code">
                  ({{ element.last_error_code }})
                </span>
              </div>
              <div
                v-if="element.last_error && element.state === 'failed'"
                class="text-caption text-error mt-1"
              >
                {{ element.last_error }}
              </div>
              <v-btn
                v-if="element.state === 'permanently_invalid'"
                size="x-small"
                variant="text"
                color="primary"
                prepend-icon="mdi-reload"
                class="mt-1"
                @click="retryInvalid(element.id)"
              >
                Retry
              </v-btn>
            </v-card-text>
          </v-card>
        </div>
      </template>
    </draggable>

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
import { computed, onMounted, ref, watch } from 'vue';
import draggable from 'vuedraggable';
import {
  isQueueItemLocked,
  useQueueStore,
  type QueueItem,
} from '../stores/queue';
import { useQueueTargetStore } from '../stores/queueTarget';
import { useAuthStore } from '../stores/auth';
import { useSnackbar } from '../composables/useSnackbar';
import {
  formatQueueTime,
  queueStatePresentation,
} from '../utils/queuePresentation';
import QueueTargetSelect from './QueueTargetSelect.vue';

const queueStore = useQueueStore();
const queueTargetStore = useQueueTargetStore();
const authStore = useAuthStore();
const { showMessage } = useSnackbar();
const showClearDialog = ref(false);

const selectedDevice = computed<number | null>({
  get: () => queueTargetStore.targetDeviceId,
  set: (id) => queueTargetStore.setTarget(id),
});
const queue = computed(() => queueStore.queueFor(selectedDevice.value));
const items = computed<QueueItem[]>({
  get: () => queue.value.items,
  set: (value) => {
    const deviceId = selectedDevice.value;
    if (deviceId !== null) queueStore.setItems(deviceId, value);
  },
});
const queueCount = computed(
  () => queue.value.status?.count ?? queue.value.count
);
const softLimit = computed(
  () => queue.value.status?.soft_limit ?? queue.value.softLimit
);
const clearDisabled = computed(
  () =>
    selectedDevice.value === null ||
    queueCount.value === 0 ||
    items.value.some(isQueueItemLocked)
);

watch(
  selectedDevice,
  async (deviceId) => {
    showClearDialog.value = false;
    if (deviceId === null) return;
    await Promise.allSettled([queueStore.refreshQueue(deviceId)]);
  },
  { immediate: true }
);

onMounted(async () => {
  try {
    await queueTargetStore.fetchDevices();
  } catch (error) {
    console.error('Failed to fetch devices', error);
  }
});

function getThumbnailUrl(url: string) {
  const token = authStore.token;
  if (!token) return url;
  const separator = url.includes('?') ? '&' : '?';
  return `${url}${separator}token=${token}`;
}

function canMove(event: { draggedContext: { element: QueueItem } }) {
  return !isQueueItemLocked(event.draggedContext.element);
}

async function removeItem(itemId: number) {
  const deviceId = selectedDevice.value;
  if (deviceId === null) return;
  try {
    await queueStore.removeItem(deviceId, itemId);
    showMessage('Removed from queue');
  } catch {
    showMessage('Queue changed; refreshed the current order', true);
  }
}

function confirmClear() {
  showClearDialog.value = true;
}

async function clearQueue() {
  const deviceId = selectedDevice.value;
  if (deviceId === null) return;
  try {
    await queueStore.clearQueue(deviceId);
    showClearDialog.value = false;
    showMessage('Queue cleared');
  } catch {
    showClearDialog.value = false;
    showMessage('Queue changed; refreshed before clearing', true);
  }
}

async function retryInvalid(itemId: number) {
  const deviceId = selectedDevice.value;
  if (deviceId === null) return;
  try {
    await queueStore.retryInvalid(deviceId, itemId);
    showMessage('Occurrence queued for retry');
  } catch {
    showMessage('Failed to retry queue occurrence', true);
  }
}

async function onDragEnd() {
  const deviceId = selectedDevice.value;
  if (deviceId === null) return;
  const itemIds = items.value
    .filter((item) => !isQueueItemLocked(item))
    .map((item) => item.id);
  try {
    await queueStore.reorderQueue(deviceId, itemIds);
  } catch {
    showMessage('Queue changed; refreshed the current order', true);
  }
}
</script>

<style scoped>
.device-select {
  width: 100%;
  max-width: 300px;
}

.queue-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}

@media (min-width: 600px) {
  .queue-grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}

@media (min-width: 960px) {
  .queue-grid {
    grid-template-columns: repeat(4, minmax(0, 1fr));
  }
}

@media (min-width: 1280px) {
  .queue-grid {
    grid-template-columns: repeat(6, minmax(0, 1fr));
  }
}

.queue-grid-item {
  min-width: 0;
}

.queue-item {
  position: relative;
}

.queue-item-locked {
  border-color: rgb(var(--v-theme-info));
}

.queue-drag-handle {
  cursor: grab;
}

.queue-drag-handle:active {
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
