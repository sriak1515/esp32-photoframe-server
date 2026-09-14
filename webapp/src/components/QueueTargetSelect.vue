<template>
  <v-select
    v-model="targetDeviceId"
    :items="queueTargetStore.devices"
    :loading="queueTargetStore.loading"
    item-title="name"
    item-value="id"
    label="Queue target"
    variant="outlined"
    density="compact"
    hide-details
    clearable
    :data-testid="testId"
  ></v-select>
</template>

<script setup lang="ts">
import { computed } from 'vue';
import { useQueueTargetStore } from '../stores/queueTarget';

defineProps<{ testId: string }>();

const queueTargetStore = useQueueTargetStore();
const targetDeviceId = computed<number | null>({
  get: () => queueTargetStore.targetDeviceId,
  set: (id) => queueTargetStore.setTarget(id),
});
</script>
