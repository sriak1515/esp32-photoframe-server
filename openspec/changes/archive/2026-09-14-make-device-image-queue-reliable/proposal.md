## Why

The per-device image queue currently treats independent selection and response completion as consumption, so concurrent polls, firmware transport retries, transient source failures, and concurrent enqueue requests can duplicate or lose queue work. Queue routes also accept every valid JWT without enforcing the device identity bound to a device token, while the web UI can display queue status for a different device than the enqueue target.

## What Changes

- Replace destructive FIFO reads with a durable per-device lifecycle covering pending, claimed, leased, retryable failed, delivered, and permanently invalid queue items.
- Atomically claim queue items and replay a device's leased item during the firmware's seven-minute retry window, then use the first later poll as best-effort implicit completion without changing firmware.
- Distinguish server-observable HTTP transfer from confirmed display and document that the unchanged firmware provides no display acknowledgement.
- Classify delivery failures, preserve retryable entries with backoff, skip unavailable entries without blocking later ready work, and retain permanently invalid entries for administrator action.
- Enforce principal-aware authorization so administrator sessions can manage queues while device tokens can perform only firmware operations for their bound device.
- Make batch enqueue transactional and deterministically ordered while preserving every valid duplicate occurrence and reporting invalid occurrences individually.
- Make reorder, removal, and clear operations safe around claimed and leased items, returning conflicts rather than claiming to cancel in-flight delivery.
- Preserve explicit Immich queue overrides, protect queue-required image/cache state, and expose cache or retry failures without discarding entries.
- Unify Gallery and QueueTab around one visible, session-scoped target device and prevent stale requests from mixing queue state between devices.
- Migrate existing queue rows in place as pending while preserving row identity, device ownership, image references, source snapshots, and FIFO order.

## Capabilities

### New Capabilities

- `device-image-queue`: Defines queue authorization, lifecycle, best-effort delivery leases, retry and invalid-item behavior, atomic ordering and batch insertion, cache/date interaction, migration compatibility, and target-device UI behavior.

### Modified Capabilities

None. The existing `immich-date-filtering` queue override and `server-authoritative-device-mode` delivery bookkeeping remain in force.

## Impact

- Backend queue model, migration, service, loader, handlers, authentication middleware, `/image` delivery integration, Immich cache retention, and related synchronization cleanup.
- Existing `/api/devices/:deviceId/queue` routes and `/image` protocol retain their paths; queue responses gain lifecycle and per-occurrence result information.
- Queue and Gallery Pinia/Vue state becomes keyed to one shared target device.
- Existing ESP32 firmware remains unchanged; its three-request transport retry behavior defines the seven-minute lease window.
- New backend concurrency, restart, migration, authorization, cache/date, and frontend device-selection tests are required.
