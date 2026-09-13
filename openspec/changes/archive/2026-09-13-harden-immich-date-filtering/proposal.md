## Why

Immich date ranges are currently parsed permissively and enforced inconsistently, allowing an invalid reversed range to look like a successful empty sync and destructively remove local images, cache records, and queue references. Date metadata and eligibility also drift across sync, selection, gallery, queue, and cache paths, so valid filters do not reliably describe what can be displayed.

## What Changes

- Validate Immich date settings as one pair before persistence, accepting empty, one-sided, and same-day ranges while rejecting malformed or reversed ranges without partial updates.
- Represent eligibility using a nullable capture-local calendar date and an inclusive user range implemented with an exclusive next-day upper boundary.
- Normalize one shared Immich date policy across synchronization, normal and collage selection, gallery access, queue behavior, and cache population.
- Refresh newly available or corrected date metadata for existing Immich image rows while retaining a known stored date when an upstream response omits date metadata.
- Treat exact Immich search bounds as a retrieval optimization and verify all returned assets with the local policy, including real albums and Memories.
- Abort an Immich sync before reconciliation when its date policy is invalid, and preserve each failed album while independently reconciling albums fetched successfully.
- Apply a valid setting change immediately to read and delivery paths while deferring destructive materialization cleanup until the next sync.
- Allow explicitly queued Immich images to override the date policy, retain their image and cache records while referenced, and clean out-of-range state after the final queue reference is removed.
- Surface invalid persisted ranges and block Immich synchronization and delivery until they are corrected.

## Capabilities

### New Capabilities
- `immich-date-filtering`: Defines validated Immich capture-date ranges, consistent eligibility enforcement, safe synchronization reconciliation, metadata refresh, and explicit queue overrides.

### Modified Capabilities

None.

## Impact

- Backend services and handlers for Immich synchronization, album reconciliation, photo selection, gallery access, image delivery, queue management, cache population, and settings persistence.
- Immich image persistence gains a nullable indexed capture-date field with a best-effort migration from existing `photo_taken_at` values.
- The settings API returns a client error for invalid Immich date pairs and the settings UI must retain and display the failure rather than treating the values as saved.
- Existing valid settings remain compatible. Existing invalid settings preserve local state but prevent Immich synchronization and delivery until corrected.
- No date filtering changes are introduced for gallery, Google Photos, Synology, or topic-based sources, and no firmware or Immich server changes are required.
