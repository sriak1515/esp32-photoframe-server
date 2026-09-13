## Context

See `proposal.md` for motivation and `specs/immich-date-filtering/spec.md` for the behavioral contract. Date filtering currently consists of raw setting reads in `ImmichService.DateRange`, timestamp clauses in picker and cache queries, and mode-specific filtering in `fetchAssetsForAlbum`. The settings handler persists map entries independently, album reconciliation treats a successful empty list as authoritative, existing image rows are not refreshed by the shared upsert, and queue delivery runs before normal source selection.

The project is a frequently rebased single-user fork, so the implementation should be narrow and avoid redesigning generic source APIs where an Immich-specific adapter or predicate is sufficient.

## Goals / Non-Goals

**Goals:**
- Make invalid ranges non-destructive and visible to the user.
- Represent the user-facing calendar semantics directly and query them consistently.
- Share one immutable policy within each operation without introducing a broad filtering framework.
- Preserve the existing per-album synchronization failure boundary.
- Support queue override semantics without allowing orphan cleanup to cascade-delete queued images.

**Non-Goals:**
- Date filtering for non-Immich sources or per-device date ranges.
- General transactional redesign of the settings API.
- General metadata refresh beyond the timestamp and capture-date fields needed here.
- A new timezone setting or exact reconstruction of historical local dates during migration.
- Changes to cache budget eviction, explicit Clear operations, album deselection, or the timezone used to choose today's Memories lane.
- New firmware behavior or changes to the Immich server.

## Decisions

### Persist a capture-local date separately

Add a nullable indexed `photo_taken_date` field to images. Store it canonically as `YYYY-MM-DD`; lexical ordering then matches calendar ordering in SQLite. Derive Immich values first from the date portion of `localDateTime`, which Immich defines as timezone-agnostic photographer-local time, and fall back to the unconverted date portion of `dateTimeOriginal`.

Keep `photo_taken_at` for display and timestamp behavior. A configured timezone was rejected because one library can contain captures from many timezones, while timestamp-only comparisons were rejected because they shift local dates across UTC midnight.

The field is available on the shared image model but is populated and filtered only for Immich in this change.

### Use one small immutable policy value

Introduce an Immich-specific policy value containing optional lower and exclusive-upper date strings. Construct it from both settings in one operation-level read, validate strict date-only syntax, ensure lower is not later than the entered upper date, and convert the entered upper date to the following calendar date.

The policy exposes in-memory eligibility and a qualified SQL scope. A missing date is eligible only when both bounds are absent. Sync, one image request, one gallery request, one queue operation, and one cache population cycle each retain one policy snapshot rather than rereading settings inside loops or callbacks.

A general source-filter abstraction was rejected as unnecessary scope.

### Validate before settings persistence

Before writing any settings from an update request, merge submitted Immich date keys with their persisted counterparts and validate the resulting pair. If invalid, return a client error before writing any setting from that request. Persist the two date keys in one database transaction and issue change notifications after commit.

Other setting keys retain the current persistence flow. A general atomic settings update was rejected to keep this change focused.

### Normalize assets before applying eligibility

Map every fetched Immich asset to normalized timestamp and capture-date metadata, regardless of source mode. For assets whose response has no usable date, batch-load existing rows by external ID and use a known stored capture date as the effective date. This both preserves metadata omitted by an endpoint and permits consistent eligibility evaluation. New genuinely undated assets are excluded only while a bound is active.

Extend the shared remote-asset upsert data sufficiently to update known incoming timestamp and capture-date values on existing Immich rows. Nil incoming values do not clear known stored values. Unrelated mutable metadata remains unchanged.

### Keep Immich search as an optimization with local verification

All and Favorites continue sending exact RFC3339 search bounds to Immich to avoid fetching the entire library. Real albums and Memories are filtered from their returned assets because their retrieval APIs do not provide equivalent bounds. Every returned mode is then passed through local capture-date eligibility before reconciliation.

This accepts that Immich owns candidate retrieval and may have version-specific edge behavior, while local verification prevents over-returned assets from entering the eligible snapshot. Widened queries, unbounded fallback searches, and a new Immich-version negotiation layer were rejected as disproportionate for this fork.

### Validate once before per-album synchronization

Build the policy before entering `SyncAlbumSource`; an invalid policy returns an error before disabled-membership pruning, remote fetches, or reconciliation. Pass an immutable Immich album-source adapter holding that policy through the existing sync.

Keep the existing per-album error boundary: `upsertAlbumAssets` runs only for a successful fetch, so a failed album retains its old membership while successful albums reconcile normally. A sync-wide prefetch and transaction were rejected because they substantially restructure the album engine and are not needed to prevent the reversed-range failure.

### Treat queue references as live references with an explicit override

Normal selection, collage picks, mixed gallery count/list queries, and normal cache buckets use the policy scope. Queue insertion intentionally accepts an existing Immich row regardless of valid-policy eligibility. Queue delivery validates that the policy itself is valid but allows the queued item to bypass its date bounds. Queue pre-cache has the same explicit exception.

Change Immich orphan collection so an image is deletable only when it has neither album membership nor queue reference. Direct thumbnail access permits an otherwise ineligible image while it has a queue reference, preserving queue UI previews.

After queue consumption, removal, or clear, inspect affected Immich image IDs. If an image has no membership, no remaining queue reference, and is ineligible under the valid policy, delete its cache file before hard-deleting the row so foreign-key cascades do not discard the path first. If the policy is invalid, preserve state and defer cleanup.

### Defer destructive range reconciliation until synchronization

A valid settings save affects all subsequent read, selection, queue-delivery, and cache-candidate operations immediately. It does not itself launch a sync or delete catalog state. The next manual or scheduled sync performs membership reconciliation and orphan cleanup.

This avoids hidden expensive work and preserves current synchronization controls. Existing cached bytes may remain temporarily, but loaders cannot use them to bypass eligibility except through a valid explicit queue override.

### Fail closed for invalid persisted policy

An invalid persisted pair produces a typed configuration error. Immich synchronization, normal delivery, queue insertion, and queue delivery stop without consuming queue entries or mutating synchronized state. The settings API and existing sync status/error presentation surface the cause so it can be corrected.

Queue management and thumbnails remain available for inspecting preserved entries, but invalid configuration cannot be interpreted as either an empty or unbounded policy.

## Risks / Trade-offs

- [Exact Immich search bounds may omit a boundary asset because its inclusivity is undocumented] -> Keep local verification for over-returning and cover the expected contract with client tests; accept rare under-returning as the chosen simplicity trade-off.
- [Backfilled dates may not match the original capture-local day] -> Treat migration output as best effort and correct it from Immich metadata on later successful syncs.
- [A temporarily omitted upstream date can leave stale local metadata] -> Preserve known dates deliberately to avoid destructive churn; overwrite them whenever a later response supplies a usable value.
- [Queue-aware liveness adds cleanup paths] -> Centralize the final-reference cleanup and test consumption, individual removal, clear, multiple devices, and retained album memberships.
- [Valid filter changes leave temporary out-of-range rows and files] -> Enforce eligibility immediately at all read and delivery paths, then reclaim materialized state during the next sync.
- [Legacy invalid settings can stop Immich frames after upgrade] -> Preserve all data, expose a specific configuration error, and allow correction through settings without requiring database access.

## Migration Plan

1. Add the nullable capture-date column and index, then backfill a best-effort `YYYY-MM-DD` value from existing `photo_taken_at` values without deleting rows.
2. Deploy policy validation and metadata normalization before enabling capture-date-based pruning.
3. Existing empty, partial, and ordered date settings become active under the new semantics automatically.
4. Existing malformed or reversed settings remain stored and visible but block Immich synchronization and delivery until corrected.
5. Subsequent successful syncs refresh reliable capture-local dates and reconcile eligible materialized state per album.

Rollback removes the new policy behavior before dropping the column. The down migration may drop the capture-date field and index; existing `photo_taken_at`, date setting strings, image rows, and memberships remain usable by the prior version.
