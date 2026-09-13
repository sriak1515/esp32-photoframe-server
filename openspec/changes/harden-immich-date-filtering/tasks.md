## 1. Date Model And Policy

- [x] 1.1 Add an up/down database migration and model field for nullable indexed `photo_taken_date`, including a non-destructive best-effort backfill from `photo_taken_at`.
- [x] 1.2 Implement the immutable Immich date policy with strict empty, partial, malformed, reversed, same-day, next-day-exclusive, unknown-date, in-memory, and qualified-query behavior.
- [x] 1.3 Add focused policy tests covering calendar boundaries, leap dates, offset-crossing-midnight metadata, unknown dates, and invalid settings reads.

## 2. Settings Validation

- [x] 2.1 Validate the prospective Immich date pair before any settings update, merging omitted date keys with persisted counterparts and returning a client error for invalid pairs.
- [x] 2.2 Persist both date keys transactionally and emit settings notifications only after commit without redesigning unrelated settings persistence.
- [x] 2.3 Update the settings UI to retain entered values and present backend validation failures, and add handler/store tests proving invalid requests leave both date settings unchanged.

## 3. Synchronization And Metadata

- [x] 3.1 Normalize Immich capture dates from `localDateTime` with `dateTimeOriginal` fallback, and carry capture-date metadata through the remote asset representation.
- [x] 3.2 Batch-resolve existing Immich dates for upstream assets with omitted metadata and refresh existing rows only when upstream supplies known timestamp or capture-date values.
- [x] 3.3 Capture one valid policy before Immich synchronization, pass it through an Immich-specific album source, and abort before fetch or reconciliation when the policy is invalid.
- [x] 3.4 Apply exact server search bounds to All and Favorites and local eligibility verification to every returned All, Favorites, real-album, and Memories asset.
- [x] 3.5 Add sync tests for every source mode, metadata correction and preservation, successful empty results, invalid-policy non-pruning, and independent preservation of a failed album.

## 4. Selection, Gallery, And Cache Enforcement

- [x] 4.1 Replace timestamp range arguments in normal and collage Immich selection with the shared capture-date query scope, including all orientation and history fallbacks.
- [x] 4.2 Apply the policy consistently to mixed-source gallery count and page queries while leaving non-Immich rows unaffected.
- [x] 4.3 Replace cache-specific date SQL with the policy scope and ensure ordinary cache lookup or population cannot bypass delivery eligibility.
- [x] 4.4 Add tests proving immediate post-save enforcement in selection, every collage slot, gallery counts/pages, and cache candidates before synchronization prunes stored rows.

## 5. Queue Override And Cleanup

- [x] 5.1 Allow explicit queue insertion, presentation, pre-caching, and delivery to override valid date eligibility while blocking insertion and delivery without consuming entries when the policy is invalid.
- [x] 5.2 Permit direct thumbnails for ineligible Immich images with an active queue reference while keeping them out of normal gallery results.
- [x] 5.3 Make Immich orphan collection retain image and cache state referenced by any queue entry.
- [x] 5.4 Implement final-queue-reference cleanup after consumption, individual removal, and clear, deleting physical cache files before their records and image rows when the image is ineligible and has no album membership.
- [x] 5.5 Add queue tests for new out-of-range insertions, policy changes after insertion, multiple device references, thumbnails, invalid-policy preservation, successful consumption, manual removal, clear, and retained album membership.

## 6. Compatibility And Verification

- [x] 6.1 Add migration tests showing existing image identity, timestamps, memberships, queues, and cache records survive capture-date backfill and rollback expectations are documented.
- [x] 6.2 Add upgrade-path tests showing valid legacy settings continue working and malformed or reversed persisted settings preserve all local state while surfacing a corrective error.
- [x] 6.3 Run backend unit and integration tests plus webapp type checking and tests, fixing only regressions introduced by this change.
- [x] 6.4 Verify the completed behavior against every scenario in `specs/immich-date-filtering/spec.md` and document any accepted Immich exact-bound limitation in user-facing help or release notes where appropriate.
