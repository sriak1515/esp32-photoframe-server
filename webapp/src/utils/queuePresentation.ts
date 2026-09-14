import type { QueueItem } from '../stores/queue';

export function queueStatePresentation(item: QueueItem) {
  switch (item.state) {
    case 'claimed':
      return {
        label: 'Preparing',
        color: 'warning',
        icon: 'mdi-progress-clock',
      };
    case 'leased':
      return { label: 'Leased', color: 'info', icon: 'mdi-transfer' };
    case 'failed':
      return {
        label: 'Retry scheduled',
        color: 'warning',
        icon: 'mdi-reload-clock',
      };
    case 'permanently_invalid':
      return { label: 'Invalid', color: 'error', icon: 'mdi-alert-circle' };
    default:
      return { label: 'Pending', color: 'primary', icon: 'mdi-clock-outline' };
  }
}

export function formatQueueTime(value?: string): string {
  if (!value) return '';
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: 'medium',
    timeStyle: 'short',
  }).format(new Date(value));
}
