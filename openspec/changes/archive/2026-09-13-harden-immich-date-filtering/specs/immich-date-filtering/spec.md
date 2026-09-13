## Purpose

Define safe, consistent calendar-date eligibility for Immich photos across synchronization, browsing, delivery, queues, and local caching.

## ADDED Requirements

### Requirement: Date range settings are validated as one pair
The system SHALL accept an empty Immich date range, a range with only a lower or upper bound, and an inclusive range whose lower date is not later than its upper date. Each non-empty bound MUST be a valid `YYYY-MM-DD` calendar date. The system MUST reject malformed or reversed ranges before changing either persisted date bound.

#### Scenario: Empty range disables filtering
- **WHEN** both Immich date bounds are empty
- **THEN** the system persists the empty pair and considers all capture dates eligible

#### Scenario: One-sided range is accepted
- **WHEN** exactly one valid Immich date bound is supplied
- **THEN** the system persists the one-sided range and applies only that bound

#### Scenario: Same-day range is accepted
- **WHEN** the lower and upper bounds contain the same valid date
- **THEN** the system persists a range that includes that entire capture-local calendar day

#### Scenario: Partial update is validated against persisted state
- **WHEN** a settings request supplies only one Immich date bound
- **THEN** the system validates the resulting pair using the submitted bound and the persisted counterpart

#### Scenario: Malformed or reversed range is rejected atomically
- **WHEN** either bound is malformed or the lower date is later than the upper date
- **THEN** the settings request fails with a client error and neither persisted date bound changes

### Requirement: Eligibility uses capture-local calendar dates
The system SHALL determine an Immich asset's date eligibility from its capture-local calendar date without converting that date through UTC or a configured timezone. The entered lower and upper dates SHALL both be inclusive, implemented canonically as a lower-inclusive and next-day-exclusive interval.

#### Scenario: Boundary dates are included
- **WHEN** an asset's capture-local date equals either entered boundary
- **THEN** the asset is eligible

#### Scenario: Date after upper boundary is excluded
- **WHEN** an asset's capture-local date is the calendar day after the entered upper boundary
- **THEN** the asset is ineligible regardless of timestamp precision or timezone offset

#### Scenario: Capture-local date crosses UTC midnight
- **WHEN** an asset's capture-local date differs from the UTC date of its timestamp
- **THEN** eligibility follows the capture-local date

#### Scenario: Unknown date under active filtering
- **WHEN** either date bound is active and an asset has no usable capture-local date
- **THEN** the asset is ineligible

#### Scenario: Unknown date without filtering
- **WHEN** both date bounds are empty and an asset has no usable capture-local date
- **THEN** the asset remains eligible

### Requirement: Date metadata is persisted and refreshed safely
The system SHALL persist a nullable capture-local date independently from the display timestamp for Immich images. A successful synchronization MUST update an existing image when upstream supplies a newly available or corrected date, and MUST retain a known stored date when the corresponding upstream response omits usable date metadata.

#### Scenario: Newly available date refreshes an existing image
- **WHEN** a synchronized Immich asset already exists locally without a capture date and upstream now supplies one
- **THEN** the existing row receives the capture date without creating a duplicate image or membership

#### Scenario: Corrected date refreshes an existing image
- **WHEN** upstream supplies a usable capture date different from the one stored for an existing Immich asset
- **THEN** the existing row is updated to the newly supplied date

#### Scenario: Omitted date preserves known metadata
- **WHEN** upstream omits usable date metadata for an existing image with a known stored date
- **THEN** the stored date remains unchanged and is used to evaluate eligibility

### Requirement: One eligibility rule governs every Immich source mode
The system SHALL apply the same local date eligibility rule to assets obtained from real albums, All, Favorites, and Memories. Immich server-side search bounds MAY reduce the All and Favorites candidate sets, but returned assets MUST still pass local eligibility evaluation before reconciliation.

#### Scenario: Every source mode evaluates the same fixture consistently
- **WHEN** equivalent dated assets are returned by real albums, All, Favorites, and Memories
- **THEN** each asset receives the same eligibility result

#### Scenario: Immich search over-returns an asset
- **WHEN** the Immich search API returns an asset outside the configured date range
- **THEN** local eligibility evaluation excludes it from the synchronized eligible set

### Requirement: Invalid policy and album failures cannot authorize destructive pruning
The system MUST validate and snapshot the Immich date policy before starting synchronization. An invalid policy MUST prevent upstream synchronization and reconciliation. A successfully fetched album MAY reconcile independently, but an album fetch, pagination, or response-decoding failure MUST preserve that album's prior memberships and images.

#### Scenario: Invalid persisted policy blocks synchronization
- **WHEN** persisted Immich date settings are malformed or reversed
- **THEN** synchronization reports a configuration error without fetching assets, pruning memberships, deleting images, or deleting associated queue and cache state

#### Scenario: One album fails while another succeeds
- **WHEN** one enabled album is fetched successfully and another enabled album fails
- **THEN** the successful album is reconciled and the failed album retains its prior memberships and images

#### Scenario: Successful empty album is authoritative
- **WHEN** an album fetch completes successfully and its eligible result is empty
- **THEN** the system may remove that album's prior memberships and clean images that have no remaining live reference

### Requirement: Active eligibility is enforced consistently
For a valid policy, the system SHALL enforce Immich date eligibility during normal and collage selection, gallery counting and listing, image delivery, and cache population. Applying an Immich filter MUST NOT change the visibility or behavior of non-Immich sources.

#### Scenario: Normal and collage selection
- **WHEN** a device requests an Immich image under an active range
- **THEN** every normally selected or collage-composed image satisfies the date policy

#### Scenario: Mixed-source gallery is filtered
- **WHEN** the gallery contains Immich and non-Immich images under an active range
- **THEN** gallery counts and pages exclude ineligible Immich images and retain otherwise visible non-Immich images

#### Scenario: Cache population uses eligible candidates
- **WHEN** normal Immich cache population runs under an active range
- **THEN** it selects only eligible images

#### Scenario: Valid setting takes effect before synchronization
- **WHEN** a valid date range is persisted
- **THEN** subsequent selection, gallery, delivery, and cache operations use it immediately while destructive catalog cleanup waits for synchronization

### Requirement: Explicit queue entries override valid date eligibility
An explicit queue entry SHALL authorize its referenced Immich image for queue display, pre-caching, and delivery even when the image is outside a valid active range or has an unknown capture date. The override MUST NOT make the image eligible for normal gallery listing, normal selection, or ordinary cache population.

#### Scenario: User queues an ineligible image
- **WHEN** a user explicitly queues an existing out-of-range Immich image
- **THEN** the queue accepts the image and may pre-cache and deliver it

#### Scenario: Valid policy changes after insertion
- **WHEN** a queued Immich image becomes ineligible under a later valid range
- **THEN** it remains visible in the queue and deliverable through the queue override

#### Scenario: Queued thumbnail remains available
- **WHEN** an out-of-range Immich image has an active queue reference
- **THEN** its thumbnail remains available for queue presentation while it remains absent from normal gallery results

#### Scenario: Invalid policy does not use the override
- **WHEN** the persisted date policy is invalid
- **THEN** Immich queue insertion and delivery are blocked, existing queue entries are preserved, and no entry is consumed because of the configuration error

### Requirement: Queue references protect materialized state
The system MUST retain an Immich image row and required cache state while any queue entry references it, even after synchronization removes all album memberships. After the final queue reference is consumed or explicitly removed, an out-of-range image with no remaining membership MUST be removed together with its cache record and physical cache file.

#### Scenario: Synchronization encounters queued out-of-range image
- **WHEN** a successful synchronization removes the final album membership from an out-of-range image that is still queued
- **THEN** the image row, queue entry, and required cached bytes are preserved

#### Scenario: Final queue reference is consumed
- **WHEN** successful delivery consumes the final queue reference to an out-of-range image with no remaining album membership
- **THEN** the image, cache record, and physical cache file are cleaned up

#### Scenario: One of several queue references is removed
- **WHEN** an image still has another active queue reference after one reference is removed
- **THEN** its image and cache state remain available

### Requirement: Existing installations migrate without destructive reinterpretation
The system SHALL preserve existing valid date settings and SHALL best-effort backfill capture-local dates for existing image rows. Existing malformed or reversed settings MUST remain visible for correction, preserve local Immich state, and block Immich synchronization and delivery rather than being silently treated as unbounded or empty.

#### Scenario: Existing timestamp is backfilled
- **WHEN** the capture-date migration encounters an image with a stored photo timestamp
- **THEN** it derives a best-effort calendar date without deleting or duplicating the image

#### Scenario: Existing invalid settings are encountered
- **WHEN** the upgraded server loads malformed or reversed Immich date settings
- **THEN** it reports the invalid configuration and preserves images, memberships, queues, cache records, and cache files until the user corrects the range
