## Purpose

Define secure, restart-safe, and deterministic per-device image queue behavior across management APIs, unchanged firmware polling, source failures, local cache availability, and web device selection.

## ADDED Requirements

### Requirement: Queue access follows authenticated principal type
The system SHALL distinguish authenticated administrator sessions from device tokens. An administrator session SHALL be permitted to manage the queue of any existing device. A device token MUST NOT access queue management APIs and MUST be permitted to fetch images only for its bound device; an unbound device token MUST NOT perform queue delivery.

#### Scenario: Administrator manages a device queue
- **WHEN** an authenticated administrator session requests a queue operation for an existing device
- **THEN** the system authorizes the operation for that device

#### Scenario: Device token requests queue administration
- **WHEN** a device token requests a queue list, check, enqueue, reorder, remove, clear, or status operation
- **THEN** the system rejects the request with HTTP 403 without performing the operation

#### Scenario: Bound device fetches an image
- **WHEN** a device token bound to device A requests the firmware image endpoint
- **THEN** the system performs queue delivery only for device A and does not accept a route or request override for another device

#### Scenario: Unbound device token fetches an image
- **WHEN** a device token without a bound device requests queue-backed image delivery
- **THEN** the system rejects queue delivery without reading or changing any device queue

### Requirement: Queue entries have a durable delivery lifecycle
Each queue entry SHALL have one durable state among pending, claimed, leased, failed, and permanently invalid. Successful best-effort completion SHALL transition through delivered before the queue row is removed. The system SHALL persist attempt and timing metadata needed to recover claims, leases, and retries after restart.

#### Scenario: New queue occurrence is accepted
- **WHEN** a valid image occurrence is inserted into a device queue
- **THEN** the system stores it in pending state with no active claim or lease

#### Scenario: Entry is reserved for processing
- **WHEN** a device poll selects an eligible pending or retry-due entry
- **THEN** the system atomically changes that entry to claimed before loading or processing it

#### Scenario: Processed response is offered
- **WHEN** a claimed entry is successfully loaded and processed for the requesting device
- **THEN** the system changes it to leased with a lease expiry before or as the response is offered

#### Scenario: Best-effort delivery completes
- **WHEN** the device's first image poll after the lease expiry advances the queue
- **THEN** the system marks the leased occurrence delivered, records device history, removes the queue row, and applies final-reference cleanup

### Requirement: Claims prevent concurrent duplicate selection
Queue selection and claim SHALL be one atomic decision scoped to the authenticated device. At most one request SHALL create the initial claim for a queue occurrence, and concurrent polls MUST NOT claim later occurrences while that device has an active claim or lease.

#### Scenario: Concurrent polls see a pending queue
- **WHEN** two requests for the same device concurrently poll a queue whose first occurrence is pending
- **THEN** exactly one request claims that occurrence and the other request neither claims nor serves a later occurrence

#### Scenario: Different devices queue the same image
- **WHEN** different devices concurrently poll queue occurrences that reference the same image
- **THEN** each device may independently claim its own occurrence

### Requirement: Firmware retries replay one leased occurrence
The system SHALL maintain a seven-minute lease for a successfully offered queue occurrence. Every subsequent image poll from that device during the active lease SHALL replay the same occurrence and MUST NOT consume it or advance to another queue occurrence. This behavior MUST require no firmware changes.

#### Scenario: Firmware retries after an interrupted response
- **WHEN** a device repeats its image GET during the seven-minute lease
- **THEN** the system serves the leased occurrence again and leaves its queue position unchanged

#### Scenario: Device polls after the retry window
- **WHEN** a device with a leased occurrence makes its first image request after the seven-minute lease expires
- **THEN** the system best-effort completes that occurrence before selecting the next eligible occurrence

#### Scenario: Device never polls again
- **WHEN** a device receives a leased occurrence and makes no later image request
- **THEN** the system retains the leased queue occurrence and does not claim that it was displayed

### Requirement: Delivery status is honest about unchanged firmware
The system SHALL describe a complete successful HTTP response only as a best-effort transfer and MUST NOT claim durable receipt, decoding, display, or panel refresh. The system SHALL NOT require a new firmware acknowledgement request, header, or endpoint.

#### Scenario: Server response completes
- **WHEN** the server finishes a complete HTTP 200 image response without a response-writer error
- **THEN** the occurrence remains leased until a later post-expiry poll implicitly completes it

#### Scenario: Firmware fails after receiving the body
- **WHEN** the unchanged firmware receives a nonempty HTTP 200 response but later fails to decode, process, or display it
- **THEN** the server makes no guarantee that it can detect that failure and does not describe the occurrence as acknowledged by the device

### Requirement: Retryable failures preserve queue work
The system SHALL classify temporary loading, upstream, cache, conversion, processing, and response-write failures as retryable. A retryable occurrence SHALL enter failed state with an attempt count, diagnostic reason, and next-attempt time; it MUST NOT be removed because of that failure.

#### Scenario: Temporary source failure occurs
- **WHEN** an upstream source or local cache cannot temporarily provide a queued image
- **THEN** the occurrence enters failed state and becomes eligible again after backoff

#### Scenario: Response write fails
- **WHEN** writing a queue-backed image response reports an error
- **THEN** the occurrence is not completed or removed and remains eligible for retry according to its failure metadata

#### Scenario: Failed head is waiting for retry
- **WHEN** the lowest-position occurrence is in failed state and its retry time has not arrived
- **THEN** the system may claim the next ready queue occurrence without changing the failed occurrence's relative position

#### Scenario: No queue occurrence is ready
- **WHEN** all queue occurrences are leased, claimed, retry-delayed, or permanently invalid
- **THEN** the device may receive normal source rotation without consuming or modifying those occurrences

### Requirement: Permanently invalid entries are visible and non-blocking
The system SHALL classify deterministic malformed or stale queue data that cannot be delivered without correction as permanently invalid. Permanently invalid occurrences MUST NOT block later ready work, MUST remain visible with an administrator-safe reason, and MUST remain stored until an administrator removes or explicitly retries them.

#### Scenario: Referenced image cannot be resolved
- **WHEN** a queue occurrence has a missing image relation, unsupported source, irreconcilable source mismatch, or missing required source identifier
- **THEN** the system marks that occurrence permanently invalid rather than silently deleting it

#### Scenario: Administrator retries an invalid entry
- **WHEN** an administrator explicitly retries a permanently invalid occurrence after correcting its data or source
- **THEN** the system returns the occurrence to pending state and clears obsolete delivery ownership while retaining prior attempt diagnostics as appropriate

### Requirement: Batch insertion is ordered, duplicate-preserving, and transactionally safe
The enqueue API SHALL evaluate every requested image occurrence independently, preserve every valid duplicate occurrence in request order, and return every rejected occurrence with a reason. All accepted occurrences from one request SHALL be inserted in one transaction; a database failure MUST roll back every accepted occurrence from that request.

#### Scenario: Request contains duplicate images
- **WHEN** a request submits image IDs `[12, 12, 25]` and all three occurrences are valid
- **THEN** the system creates three distinct pending queue entries in exactly that order

#### Scenario: Request mixes valid and invalid occurrences
- **WHEN** a request contains valid occurrences and missing or invalid image occurrences
- **THEN** the system inserts all valid occurrences as one ordered batch and reports each rejected occurrence and reason

#### Scenario: Batch persistence fails
- **WHEN** any database operation required to insert the accepted occurrences fails
- **THEN** none of the request's occurrences are inserted

#### Scenario: Batches enqueue concurrently
- **WHEN** multiple enqueue requests for one device commit concurrently
- **THEN** each request's accepted occurrences form a complete ordered group with unique positions and no interleaving or collision inside that group

### Requirement: Queue ordering is atomic and occurrence-based
Delivery order SHALL be determined by queue occurrence position with stable occurrence ID as the tie-breaker. Reorder requests SHALL identify occurrences by queue item ID, MUST operate atomically, and MUST reject stale, repeated, foreign-device, missing, claimed, or leased item IDs without changing order.

#### Scenario: Duplicate images are reordered
- **WHEN** a queue contains multiple occurrences of the same image and an administrator submits their distinct queue item IDs
- **THEN** the system reorders those occurrences independently

#### Scenario: Reorder request is stale
- **WHEN** a reorder request does not provide the exact reorderable occurrence set for that device
- **THEN** the system returns HTTP 409 and preserves the prior order

#### Scenario: Reorder includes in-flight occurrence
- **WHEN** a reorder request includes a claimed or leased occurrence
- **THEN** the system returns HTTP 409 and preserves the prior order

### Requirement: Administrative removal does not misrepresent in-flight cancellation
An administrator SHALL be permitted to remove pending, failed, or permanently invalid occurrences. Individual removal or clear that would affect a claimed or leased occurrence MUST return HTTP 409 and MUST leave the queue unchanged.

#### Scenario: Administrator removes pending occurrence
- **WHEN** an administrator removes a pending queue item belonging to the route device
- **THEN** the system removes that occurrence and performs any required final-reference cleanup

#### Scenario: Administrator clears queue with active lease
- **WHEN** an administrator clears a queue containing a claimed or leased occurrence
- **THEN** the system returns HTTP 409 and does not partially clear the queue

### Requirement: Queue references preserve required source and cache state
Every non-delivered queue occurrence SHALL remain a live reference for source synchronization and cache cleanup. Cache pruning MUST NOT evict bytes required by pending, claimed, leased, failed, or permanently invalid queue occurrences. A cache miss or corrupt cache entry SHALL use the upstream source when possible and SHALL become a retryable failure when the upstream source is temporarily unavailable.

#### Scenario: Queued Immich image is pruned from its album
- **WHEN** synchronization removes the last album membership from an Immich image that still has a queue occurrence
- **THEN** the image row, queue-visible thumbnail, and required cache state remain available

#### Scenario: Queue-required cache entry reaches pruning order
- **WHEN** cache limits would otherwise evict bytes referenced by a queue occurrence
- **THEN** the system retains those bytes and considers another eligible cache entry

#### Scenario: Cached file is corrupt
- **WHEN** loading a queued image finds an unreadable cached file and the upstream source is available
- **THEN** the system discards or repairs the corrupt cache state and attempts to load the same occurrence upstream

### Requirement: Existing Immich date override remains authoritative
Queue insertion, visibility, pre-caching, and delivery SHALL preserve the explicit queue override defined by the `immich-date-filtering` capability. Invalid persisted date configuration MUST block affected insertion and delivery without consuming or invalidating existing occurrences.

#### Scenario: Queued image becomes out of range
- **WHEN** a valid Immich date policy changes after an occurrence was queued
- **THEN** the occurrence remains deliverable and continues protecting its required image and cache state

#### Scenario: Immich policy is malformed
- **WHEN** the persisted Immich date policy is invalid during insertion or queue delivery
- **THEN** the operation reports the configuration error and preserves all existing queue, image, and cache state

### Requirement: Queue presentation exposes actionable lifecycle state
Queue management responses SHALL expose each occurrence's lifecycle state and administrator-safe failure information. Queue membership checks SHALL be scoped to the requested device and SHALL report an image as queued when at least one non-delivered occurrence references it.

#### Scenario: Duplicate occurrence states differ
- **WHEN** two queue occurrences reference the same image and have different lifecycle states
- **THEN** the queue list exposes both occurrence IDs and their respective states

#### Scenario: Gallery checks queue membership
- **WHEN** the Gallery checks image IDs for the selected target device
- **THEN** the response reflects only occurrences belonging to that device

### Requirement: Gallery and QueueTab share one target device
The web application SHALL use one visible, session-scoped target device for queue list, status, membership checks, enqueue, reorder, remove, and clear actions. It SHALL automatically select a device only when exactly one device exists and MUST require explicit selection when multiple devices exist and no valid session preference is present.

#### Scenario: User changes target device
- **WHEN** the user changes the queue target in either Gallery or QueueTab
- **THEN** both views use the new device for subsequent queue status and actions

#### Scenario: Multiple devices have no preference
- **WHEN** multiple devices exist and the session has no valid target selection
- **THEN** the UI does not show device-specific queue membership or enqueue an image until the user selects a target

#### Scenario: Target changes during request
- **WHEN** an earlier queue request completes after the user has selected another target device
- **THEN** the stale response does not replace or annotate the currently selected device's queue state

### Requirement: Existing queues migrate without loss or reinterpretation
The migration SHALL preserve every existing queue row, row ID, device reference, image reference, source snapshot, creation time, and FIFO order. Existing rows SHALL become pending occurrences. If legacy positions require normalization, ordering SHALL use legacy position followed by row ID.

#### Scenario: Existing installation is upgraded
- **WHEN** the migration encounters legacy queue rows
- **THEN** every row remains present as pending and produces the same deterministic relative delivery order

#### Scenario: Server restarts with active lifecycle state
- **WHEN** the server restarts while entries are pending, claimed, leased, failed, or permanently invalid
- **THEN** durable ownership, lease, retry, and diagnostic state is retained and subsequent polls resume according to that state

### Requirement: Delivery and cleanup are completion-safe
Best-effort completion SHALL associate one device-history record with the delivered queue occurrence and SHALL remove the queue row only as part of a completion operation that cannot report success while required queue-reference cleanup has failed. Reprocessing an already-completed occurrence MUST NOT create duplicate completion effects.

#### Scenario: Completion cleanup fails
- **WHEN** final queue-reference or cache cleanup fails while completing a leased occurrence
- **THEN** the system preserves recoverable queue state and does not silently report the occurrence as fully completed

#### Scenario: Completion is retried
- **WHEN** completion of the same leased occurrence is attempted more than once
- **THEN** at most one effective device-history and queue-removal result is committed
