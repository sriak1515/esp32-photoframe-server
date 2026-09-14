## Context

See `proposal.md` for motivation and `specs/device-image-queue/spec.md` for the behavioral contract. The current queue table stores only identity, ownership, image, source, position, and creation time. Delivery reads the lowest position without reservation, performs potentially slow loading and conversion, then records history and removes the row after the server response writer returns success.

The database is SQLite in WAL mode with a 30-second busy timeout and one open connection. This serializes database calls inside one process but does not make separate reads and writes atomic, and no database transaction can remain open during upstream download, image processing, or HTTP delivery. Existing ESP32 firmware performs up to three independent GET attempts, each with a 120-second timeout and three-second retry delay, but has no queue identity or display acknowledgement. A nonempty HTTP 200 is accepted before decoding and panel refresh.

The existing Immich date-filtering capability deliberately treats queue references as eligibility overrides and materialized-state roots. The server-authoritative device capability similarly distinguishes a response being pushed from durable firmware acknowledgement. Both constraints remain unchanged.

## Goals / Non-Goals

**Goals:**

- Make all queue ownership and lifecycle decisions durable, atomic, and scoped to one authenticated device.
- Prevent concurrent polls and firmware retries from advancing through multiple queue occurrences.
- Preserve every requested duplicate occurrence and make each batch internally ordered and transactionally safe.
- Keep queue-required catalog and cache state alive until the final occurrence is completed or administratively removed.
- Make queue management and Gallery annotations consistently target one selected device.
- Preserve current routes and unchanged firmware compatibility.

**Non-Goals:**

- Prove that firmware decoded an image or refreshed the physical panel.
- Add firmware acknowledgement, request identifiers, or new firmware endpoints.
- Guarantee exactly-once physical display.
- Add a background queue worker or deliver images independently of device polls.
- Add multi-user device ownership or roles beyond distinguishing administrator sessions from device tokens.
- Retain a separate long-lived history of delivered queue rows beyond existing device history.

## Decisions

### Extend the existing queue table with lifecycle columns

Add a new migration after `000041` that extends `device_image_queue` with a constrained state and nullable lifecycle metadata. The model will include `state`, `claim_expires_at`, `lease_expires_at`, `attempt_count`, `next_attempt_at`, `last_attempt_at`, `last_error_code`, and an administrator-safe `last_error`. Existing rows are backfilled as pending.

Keep `id`, `device_id`, `image_id`, `position`, `source`, and `created_at`. Do not add uniqueness on `(device_id, image_id)`, because repeated occurrences are intentional. Retain `(device_id, position)` uniqueness and use `(position, id)` as the stable ordering rule while migrating or diagnosing legacy data.

`delivered` is a transactional completion state rather than retained data: completion records device history, performs queue-reference cleanup, and removes the queue row. Permanently invalid rows remain in the table until administrator action.

Alternative considered: replace the table with separate jobs, attempts, and delivery tables. That models history more fully but adds joins and migration complexity without improving the unchanged firmware guarantee.

### Use compare-and-swap state transitions in short transactions

Queue service operations will perform state transitions with conditional updates and require the expected affected-row count. Selection considers entries in position/ID order that are pending or failed with `next_attempt_at` due. It updates exactly one eligible row to claimed with a bounded processing deadline.

No transaction spans loading, conversion, cache access, or HTTP writing. After a complete response write, a conditional update changes the same claimed occurrence to leased and sets an absolute lease expiry. A write or preparation failure conditionally changes it to failed. A stale worker cannot update a row whose state has since changed.

A claimed occurrence blocks another request from claiming a later item for the same device. Such a concurrent request receives a retryable response rather than waiting while holding scarce server or database resources. An expired claim is recovered as retryable before selection.

Alternative considered: rely on SQLite's single connection or an in-memory mutex. Neither survives restart or makes a multi-statement lifecycle externally inspectable; an in-memory lock may still be used only as an optimization, never as the correctness boundary.

### Treat the seven-minute lease as one firmware retry window

The first successful queue-backed HTTP transfer starts one fixed seven-minute lease. A poll during that interval atomically reserves the leased occurrence for replay, re-renders or reloads the same queue occurrence, and restores its lease without extending the original expiry. Concurrent replay requests cannot advance the queue or simultaneously own the replay transition.

On the first poll after expiry, the service completes the prior leased occurrence, then selects the next ready occurrence. If the device never returns, no timer claims the item was displayed and the leased row remains available for administration and later progression.

This deliberately interprets a later poll as best-effort progression, not acknowledgement. Seven minutes covers the firmware's maximum nominal `3 * 120 seconds + 2 * 3 seconds` attempt window with margin. It can delay advancement for devices configured to rotate more frequently than seven minutes, but it prevents one logical firmware rotation from draining multiple items.

Alternative considered: remove after the first response writer success. That preserves throughput but loses items when firmware retries. Requeue forever after lease expiry is safer in theory but makes unchanged firmware repeat one queue item indefinitely.

### Do not persist rendered response bodies

Replay is defined by queue occurrence, not byte-for-byte body identity. The server reloads and processes the same referenced image using the captured device settings for each response attempt. Existing image processing and cache paths remain authoritative, and no potentially large rendered payload is added to SQLite.

Alternative considered: persist exact rendered bytes per lease. This would stabilize overlays and reduce processing but adds filesystem lifecycle, storage limits, stale device-setting concerns, and another crash-consistency boundary. It is not needed to prevent queue advancement.

### Classify deterministic invalidity separately from transient failure

Temporary upstream/network/cache access, image conversion, rendering, and response-write errors become failed with capped exponential backoff and jitter. Use an initial delay compatible with the firmware's three-second retry interval and a bounded maximum delay. Transient failures do not become permanently invalid solely because an attempt threshold is reached.

Missing image relations, unsupported source values, irreconcilable source snapshots, or absent required stable source identifiers become permanently invalid immediately. Decode corruption is retryable when the cache can be repaired or upstream content refetched; it becomes permanently invalid only when the underlying content is deterministically unusable. Error codes are stable machine-readable categories; stored messages are sanitized for administrator display.

Failed or invalid entries are skipped during ready selection, preserving their position for later retry while allowing subsequent ready occurrences to progress. If none are ready, existing normal source rotation remains available.

Alternative considered: delete malformed rows or permanently fail after a fixed attempt count. Both can discard recoverable source outages and recreate the original loss behavior.

### Make completion and final-reference cleanup idempotent

Completion runs under the existing Immich cache filesystem mutex and one database transaction. It conditionally verifies the expired leased occurrence, writes a device-history record tied idempotently to that occurrence, transitions through delivered, and performs the existing final-reference catalog/cache cleanup before deleting the queue row. Physical cache staging follows the existing stage/restore/remove pattern so database rollback can restore filesystem state.

To make history idempotent, add a nullable queue occurrence reference or equivalent unique completion key to device history. A repeated completion attempt either observes that the occurrence has already gone or conflicts harmlessly; it cannot create a second history effect.

History remains ancillary only after an idempotent completion record exists. A required cleanup failure retains recoverable lease state and surfaces an error instead of silently deleting the queue reference.

Alternative considered: keep history insertion and queue deletion as independent best-effort calls. That permits duplicate histories and repeated delivery when either call fails.

### Validate partial batches before one insertion transaction

Preserve the request as an indexed list of occurrences. Resolve all distinct image IDs in bulk, then classify each occurrence independently. Repeated valid IDs remain repeated accepted occurrences. Missing or deterministically invalid occurrences are returned with their original request index, image ID, and reason.

Inside one transaction, read the current maximum position and insert every accepted occurrence at successive increments of ten. SQLite's serialized write path and transaction boundary ensure each batch is contiguous relative to other committed batches. Any insertion or foreign-key failure rolls back all accepted occurrences, while input-level rejections remain response data rather than transaction failures.

The existing response fields remain where practical, but `added` contains occurrence records and a new rejected collection replaces the currently hard-coded empty duplicate list. Duplicate-skipping semantics are removed.

Alternative considered: one transaction per occurrence. That provides partial database success but permits position collisions and leaves callers unable to distinguish validation rejection from interrupted persistence.

### Reorder an exact snapshot of reorderable occurrences

List responses expose state and enough metadata for the UI to disable drag/removal of claimed and leased occurrences. A reorder request contains the exact ordered item-ID set currently reorderable for that device. The service validates uniqueness, ownership, state, and set equality before changing any position.

Use a two-phase position update inside one transaction to avoid the existing unique position constraint, then assign positive increments of ten. Claimed and leased positions remain fixed; reorderable entries are placed consistently around those fixed positions or the operation conflicts if the submitted snapshot cannot preserve them. A stale response is HTTP 409 and prompts a refetch.

Clear first verifies that no claimed or leased occurrence exists, then removes all other occurrences and performs final-reference cleanup in one all-or-nothing operation. Individual remove uses the same state check and cleanup path.

Alternative considered: silently reorder only submitted IDs. That leaves omitted items at conflicting positions and lets stale clients overwrite newer queue state.

### Separate administrator and device route policy

Authentication middleware will expose a typed principal containing user identity, token subject, and bound device identity. Queue management routes require an administrator session. Firmware routes require a bound device token where device-specific operation is involved and derive the device exclusively from that principal.

Organize routes or middleware policy so device tokens cannot inherit the broad administrator `/api` surface merely because their JWT validates. Authorization occurs before target-resource lookup where practical to avoid cross-device enumeration. The current single-user sessions are treated as administrators; introducing user roles or per-user device ownership is deferred.

Alternative considered: add ownership checks only inside queue handlers. That fixes the named cross-device route but leaves device credentials authorized for unrelated administrator APIs and repeats principal logic.

### Preserve queue cache roots and date overrides

All stored queue states remain live references until removal, including permanently invalid rows, because administrator retry may make them deliverable again. Immich synchronization and thumbnail access continue using the existing queue override. Invalid date configuration blocks affected transitions without changing queue state.

Immich cache pruning excludes files referenced by any queue row. Queue pre-cache remains asynchronous and reports readiness/failure rather than making enqueue wait on a remote download. Delivery tries a valid local cache first, repairs corrupt entries when possible, then tries upstream; temporary unavailability enters failed state.

Final-reference cleanup continues only when no queue occurrence and no album membership references an out-of-range Immich image.

Alternative considered: require cache completion before enqueue commits. That would make queue API latency and atomicity depend on remote I/O and would reject useful online-only occurrences.

### Use one session-persisted frontend target and device-keyed data

Replace the independent default and active queue device refs with one target device ID persisted in session storage. Validate it after loading devices, automatically select only when exactly one device exists, and display the target near Gallery queue controls.

Queue state and membership results are keyed by device ID, or guarded by request generation and captured target ID, so a late response cannot overwrite another device's view. Every mutation captures one target at dispatch. Gallery and QueueTab update the same target, and no fallback chain chooses a different device for status than for enqueue.

Queue entries display retry, lease, and permanently invalid state. Claimed/leased controls are disabled and server-side 409 remains authoritative. Duplicate occurrences remain separate cards keyed by queue item ID.

Alternative considered: retain separate selectors and make each explicit. That avoids shared state but preserves context switching and makes Gallery annotations harder to interpret.

## Risks / Trade-offs

- [A successful HTTP body can still fail during firmware decode or display] -> Use best-effort transfer language and preserve a future path to explicit acknowledgement without claiming current guarantees.
- [A fixed seven-minute lease delays queues on very short rotation schedules] -> Prefer retry safety; expose leased-until state so the delay is observable.
- [A server crash after sending bytes but before persisting leased state causes replay] -> Accept at-least-once transfer as safer than silent loss; claim expiry recovers the occurrence.
- [A later poll may implicitly complete an image that firmware failed to display] -> Document this unavoidable no-client-change limitation and retain administrator-visible history as best effort only.
- [Skipping retry-delayed entries relaxes strict FIFO] -> Preserve positions and restore priority when retry becomes due so only availability, not order metadata, changes progression.
- [Permanent invalid rows and intentional duplicates can grow queues] -> Keep the existing soft-limit warning, expose states clearly, and provide safe administrator removal/clear.
- [Broader principal separation may reveal existing device-token dependencies on administrator APIs] -> Inventory firmware routes and add authorization regression tests before moving routes behind session-only policy.
- [Filesystem and database cleanup cannot be physically atomic] -> Reuse staged rename and rollback restoration under the cache mutex.
- [Central image handler changes increase upstream merge conflicts] -> Keep claim, replay, completion, and failure transitions in the queue service and leave the handler as orchestration.

## Migration Plan

1. Add lifecycle columns and constraints in a forward migration, backfill every legacy row as pending, and normalize duplicate or non-positive legacy positions deterministically by `(position, id)` if required.
2. Add any device-history completion key needed for idempotency while preserving existing history rows as unrelated legacy records.
3. Deploy backend principal classification and queue lifecycle behavior together so new state cannot be interpreted by old destructive reads.
4. Deploy additive queue response fields and the frontend shared target/state presentation; existing endpoint paths and firmware requests remain valid throughout.
5. Verify upgraded databases, active queues, Immich queue roots, and rollback on a copy of a migrated database.

Rollback requires a down migration that rebuilds the SQLite queue table with its legacy columns and copies all still-present queue rows in deterministic position order. Claimed, leased, failed, and permanently invalid rows become ordinary legacy rows on rollback; delivered rows have already been removed. This loses lifecycle diagnostics but does not lose active queue occurrences.
