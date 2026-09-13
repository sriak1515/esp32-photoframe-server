## Purpose

Define a per-device ownership mode that protects server-managed settings from device-originated replacement while retaining the existing best-effort firmware delivery protocol.

## ADDED Requirements

### Requirement: Per-device authoritative mode
The system SHALL store a server-authoritative mode for each device and SHALL enable it by default for both existing and newly created devices. The system SHALL allow a user to disable the mode to restore the existing bidirectional synchronization behavior for that device.

#### Scenario: Existing device is migrated
- **WHEN** the database migration is applied to an existing device
- **THEN** server-authoritative mode is enabled without altering its stored configuration, processing settings, palette, or legacy synchronization timestamp

#### Scenario: New device is created
- **WHEN** a device is added after the migration
- **THEN** server-authoritative mode is enabled for that device by default

#### Scenario: User disables authoritative mode
- **WHEN** a user disables server-authoritative mode and saves the device
- **THEN** the device resumes the existing bidirectional timestamp reconciliation workflow

### Requirement: Server-owned desired settings
While server-authoritative mode is enabled, the system SHALL treat stored device configuration, processing settings, and color or grayscale palette as server-owned desired state. Device timestamps, request headers, background refreshes, and explicit refresh operations MUST NOT replace that desired state.

#### Scenario: Device reports a newer timestamp
- **WHEN** an authoritative device requests an image with a configuration timestamp newer than the server timestamp
- **THEN** the system does not copy device configuration, processing settings, or palette into server storage

#### Scenario: Device reports incomplete processing fields
- **WHEN** an authoritative device reports processing settings that omit server-only fields such as converter, automatic mode, or epdoptimize preset
- **THEN** every stored server processing field remains unchanged

#### Scenario: Background refresh runs
- **WHEN** an inventory refresh contacts an authoritative device
- **THEN** it updates only firmware-owned inventory or telemetry and leaves all server-managed desired settings unchanged

### Requirement: Explicit device setting import
The system SHALL distinguish refreshing firmware-owned device information from importing device-managed settings. Import SHALL be unavailable while server-authoritative mode is enabled. When authoritative mode is disabled, import SHALL merge only fields represented by firmware and SHALL preserve server-only processing fields.

#### Scenario: Import is disabled in authoritative mode
- **WHEN** a user views or refreshes an authoritative device
- **THEN** the device setting import action is disabled and no managed setting is imported

#### Scenario: Inventory refresh remains available
- **WHEN** a user refreshes an authoritative device's information
- **THEN** firmware version, dimensions, board or display identity, battery state, and connectivity information can be refreshed independently of managed settings

#### Scenario: Import from a non-authoritative device
- **WHEN** a user explicitly imports settings from a device with authoritative mode disabled
- **THEN** supported firmware configuration, processing, and palette fields are imported while server-only processing fields are preserved

#### Scenario: Initial enrollment seeds desired state
- **WHEN** a new device is enrolled and has no persisted server desired state
- **THEN** the system may seed supported settings from firmware once before enabling server ownership, while filling server-only fields from server defaults

### Requirement: Persistence precedes device delivery
The system MUST validate and persist a server settings update, including its pending-delivery state, before contacting the device. If any required persistence operation fails, the system MUST return an error, MUST NOT attempt device delivery, and MUST NOT report the update as successful.

#### Scenario: Desired settings persistence fails
- **WHEN** the database fails while storing configuration, processing settings, palette, generated credentials, or pending-delivery state
- **THEN** the request fails and no configuration request is sent to the device

#### Scenario: Persistence succeeds
- **WHEN** all desired settings and pending-delivery state are persisted successfully
- **THEN** the system may attempt best-effort direct delivery

### Requirement: Complete best-effort direct delivery
For an authoritative device, direct delivery SHALL attempt the stored processing settings, palette, and device configuration through the existing firmware endpoints. The system SHALL consider the direct delivery pushed only when every required request succeeds; any unavailable device or component failure SHALL leave the update pending for deferred delivery.

#### Scenario: All direct requests succeed
- **WHEN** processing settings, palette, and device configuration are each accepted by the device
- **THEN** the system records the update as pushed and reports that the device endpoints accepted it

#### Scenario: Palette delivery fails after another component succeeds
- **WHEN** any direct component succeeds but palette delivery fails
- **THEN** the system keeps the complete desired update pending and does not report it as fully pushed

#### Scenario: Device is unavailable
- **WHEN** the server cannot contact the device during direct delivery
- **THEN** the desired settings remain saved and pending for a later image fetch

### Requirement: Deferred retry of pending settings
The system SHALL include the complete stored configuration, processing settings, and palette in the existing deferred configuration payload when an authoritative device has a pending update and successfully receives an image response. A failed image selection, queue load, image processing operation, or response construction MUST leave the update pending.

#### Scenario: Pending device fetches an image successfully
- **WHEN** an authoritative device with pending settings receives a successful image response containing the complete deferred payload
- **THEN** the system records the payload as pushed through the legacy transport

#### Scenario: Image processing fails
- **WHEN** image processing fails before a complete response with the deferred payload is produced
- **THEN** the settings remain pending for a later request

#### Scenario: Queued image fails
- **WHEN** loading or processing a queued image fails
- **THEN** synchronization status remains pending and no delivery success is recorded

### Requirement: Honest synchronization status
The system SHALL expose whether a device is server-authoritative and whether its latest settings are pending or were pushed through an existing firmware transport. The system MUST NOT describe a direct HTTP success or emitted deferred payload as durable firmware acknowledgement.

#### Scenario: Settings await delivery
- **WHEN** desired settings are saved but no complete delivery attempt has succeeded
- **THEN** the UI reports that settings are pending the next device fetch

#### Scenario: Existing endpoints accept all settings
- **WHEN** all direct delivery requests return success
- **THEN** the UI reports that settings were pushed rather than durably synchronized

#### Scenario: Deferred payload is emitted
- **WHEN** a complete deferred payload is included in a successful image response
- **THEN** the UI may report that settings were pushed through deferred delivery but does not claim durable acknowledgement

### Requirement: Existing protocol compatibility
The system SHALL implement authoritative mode without requiring firmware changes, new firmware endpoints, or removal of the existing timestamp and configuration-payload protocol. Devices with authoritative mode disabled SHALL retain their prior synchronization behavior.

#### Scenario: Existing firmware is used
- **WHEN** a device supports the current config, processing, palette, and image-fetch APIs
- **THEN** authoritative mode works without installing modified firmware

#### Scenario: Authoritative mode is off
- **WHEN** a device with authoritative mode disabled reports newer state
- **THEN** the existing reconciliation behavior remains available, subject to preserving server-only processing fields
