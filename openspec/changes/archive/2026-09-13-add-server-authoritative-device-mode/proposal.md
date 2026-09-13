## Why

Device-originated synchronization can overwrite newer server processing and configuration values, while ignored persistence errors and partial direct pushes can report success without leaving a recoverable server state. This fork needs a narrowly scoped mode in which the server is unambiguously authoritative without replacing the existing firmware protocol or creating a large divergence from upstream.

## What Changes

- Add a per-device server-authoritative setting, enabled by default for existing and new devices.
- When authoritative mode is enabled, prevent automatic pulls, explicit refreshes, and device timestamps from overwriting server-managed configuration, processing settings, or palette data.
- Separate firmware-owned inventory refresh from an explicit, ownership-aware import action, and disable device setting import while authoritative mode is enabled.
- Persist the complete server-side settings update and pending-delivery state before attempting any device push; return persistence failures instead of continuing or reporting success.
- Keep direct delivery as a best-effort convenience, include the palette alongside config and processing settings, and retain pending state after any partial or unavailable-device result.
- Retry pending settings through the existing deferred image-response payload without adding or changing firmware protocol requirements.
- Report authoritative, pushed, and pending states accurately in the web UI without claiming durable device acknowledgement.
- Preserve the existing bidirectional timestamp workflow when authoritative mode is disabled.

## Capabilities

### New Capabilities

- `server-authoritative-device-mode`: Per-device ownership policy, persistence-before-delivery guarantees, best-effort direct and deferred delivery behavior, safe refresh/import behavior, and synchronization status presentation.

### Modified Capabilities

None.

## Impact

- Database device schema and migration defaults.
- Device model and device configuration HTTP handlers.
- Device refresh/import service behavior.
- Existing PhotoFrame client calls for config, processing settings, and palette.
- Image response deferred-configuration handling and queue failure interaction.
- Device settings UI controls and delivery-status messaging.
- Backend and webapp tests; no firmware repository or protocol changes.
