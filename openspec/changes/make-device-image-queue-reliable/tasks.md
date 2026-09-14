## 1. Schema And Migration

- [x] 1.1 Add queue lifecycle constants and model fields for state, claim/lease deadlines, attempts, retry timing, and sanitized failure diagnostics.
- [x] 1.2 Add forward and rollback migrations that preserve existing queue IDs, references, timestamps, source snapshots, and deterministic `(position, id)` order while initializing legacy rows as pending.
- [x] 1.3 Add an idempotent queue-occurrence completion key to device history, preserving all legacy history rows.
- [x] 1.4 Add migration tests for populated legacy queues, duplicate image occurrences, position normalization, active-state rollback, foreign keys, and lifecycle defaults.

## 2. Authentication And Route Policy

- [x] 2.1 Introduce a typed authenticated principal that distinguishes administrator sessions from device tokens and exposes the token-bound device ID.
- [x] 2.2 Inventory protected backend and firmware routes, require administrator sessions for queue management and other administrative APIs, and retain only the firmware-required device-token surface.
- [x] 2.3 Make image delivery derive device identity exclusively from the bound device token and reject unbound device tokens before queue access.
- [x] 2.4 Add middleware and handler tests proving administrator access, device-token queue-management rejection, unbound-token rejection, and cross-device isolation without resource enumeration.

## 3. Queue Lifecycle Service

- [x] 3.1 Replace next-item reads with transactional compare-and-swap operations for initial claim, leased replay claim, claim recovery, failure, lease creation, and implicit completion.
- [x] 3.2 Implement a fixed seven-minute lease that replays the same occurrence without extending expiry and completes only on the first later device poll.
- [x] 3.3 Implement retryable error categories, attempt accounting, capped backoff with jitter, due-item selection, and deterministic permanent-invalid classification.
- [x] 3.4 Make retry-delayed and permanently invalid occurrences non-blocking while preserving their positions and falling back to normal source rotation when no occurrence is ready.
- [x] 3.5 Expose lifecycle, lease, retry, and sanitized failure metadata through queue list/status/check responses without changing existing route paths.
- [x] 3.6 Add service tests for every state transition, stale compare-and-swap rejection, lease replay/expiry, restart recovery, clock boundaries, error classification, and invalid-item retry.

## 4. Delivery And Completion

- [x] 4.1 Integrate claim and replay decisions into image delivery while keeping database transactions outside image loading, conversion, and response writing.
- [x] 4.2 Transition a completed HTTP response to leased, transition detectable load/processing/write failures to failed, and preserve server-authoritative configuration pending state on queue failures.
- [x] 4.3 Implement idempotent implicit completion that records one device-history effect, transitions through delivered, removes the queue row, and invokes final-reference cleanup safely.
- [x] 4.4 Prevent concurrent same-device initial or replay requests from serving a later occurrence and return a firmware-retryable response while another request owns the claim.
- [x] 4.5 Add handler concurrency tests for simultaneous polls, response-write failure, retries during the seven-minute lease, first post-expiry progression, process restart, completion retry, and different-device independence.

## 5. Atomic Queue Management

- [x] 5.1 Rework batch enqueue to classify each indexed request occurrence, preserve every valid duplicate, and return explicit rejected occurrences with reasons.
- [x] 5.2 Insert all accepted occurrences from a request in one ordered transaction so concurrent batches remain contiguous and any persistence failure rolls back the complete accepted set.
- [x] 5.3 Validate reorder requests as exact unique snapshots of reorderable item IDs and apply collision-free positions atomically while claimed and leased occurrences remain fixed.
- [x] 5.4 Make individual removal and clear all-or-nothing, returning HTTP 409 without mutation when claimed or leased occurrences would be affected.
- [x] 5.5 Add tests for duplicate-preserving batches, mixed valid/invalid input, transaction rollback, concurrent enqueue ordering, stale/foreign/repeated reorder IDs, and in-flight removal/clear conflicts.

## 6. Cache, Source, And Date Integration

- [x] 6.1 Make queue loading detect source mismatches, missing identifiers, stale relations, corrupt cache files, and temporary upstream failures using the lifecycle error categories.
- [x] 6.2 Exclude cache files referenced by every stored queue state from pruning and retain queue-protected image, thumbnail, and cache state during source synchronization.
- [x] 6.3 Preserve asynchronous queue pre-cache while exposing readiness or failure and falling back from corrupt/missing cache state to upstream loading when possible.
- [x] 6.4 Reuse final-reference cleanup for implicit completion, individual removal, and clear without weakening the existing Immich date-policy override or invalid-policy preservation behavior.
- [x] 6.5 Add integration tests for offline Immich retry, queue cache pruning protection, corrupt cache repair, policy changes after enqueue, invalid policy, multiple duplicate/device references, and final-reference cleanup failures.

## 7. Shared Frontend Device Target

- [x] 7.1 Replace independent default and active queue device refs with one session-storage-backed target that is validated against the current device list and auto-selected only for a single device.
- [x] 7.2 Make Gallery and QueueTab display and mutate the same target for membership, enqueue, list, reorder, remove, and clear operations.
- [x] 7.3 Key queue data by device or guard updates with captured target/request generations so stale responses cannot overwrite another device's queue or Gallery annotations.
- [x] 7.4 Present duplicate occurrences independently and display pending, retry, leased, and permanently invalid states with claimed/leased controls disabled and 409 recovery by refetch.
- [x] 7.5 Add frontend tests for zero/one/multiple-device selection, session restoration, target changes in both views, stale request completion, duplicate cards, lifecycle presentation, and matching status/enqueue targets.

## 8. Verification And Compatibility

- [x] 8.1 Run backend unit, migration, race/concurrency, and integration test suites and resolve failures without relaxing queue guarantees.
- [x] 8.2 Run frontend unit tests, type checking, formatting, and production build.
- [x] 8.3 Verify unchanged firmware request headers, response formats, three-attempt retry timing, and `/image` paths against the ignored firmware checkout without modifying firmware.
- [x] 8.4 Validate every scenario in the device-image-queue delta spec and confirm the existing Immich date-filtering and server-authoritative device-mode specifications still pass.
- [x] 8.5 Document the seven-minute best-effort transfer limitation and ensure UI/API language never claims firmware receipt or physical display acknowledgement.
