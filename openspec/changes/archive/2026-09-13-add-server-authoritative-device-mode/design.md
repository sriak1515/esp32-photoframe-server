## Context

See `proposal.md` for motivation and `specs/server-authoritative-device-mode/spec.md` for the behavioral contract.

The current server stores device config, processing settings, palette, and one `config_last_updated` timestamp on the device row. Image requests compare that timestamp with a firmware-generated timestamp and may either push server state or asynchronously pull device state. Dashboard saves persist several values without checking every database result, then call separate firmware endpoints for processing and config; palette is not included in direct delivery. Deferred delivery combines all three documents in the existing `X-Config-Payload` response header, but only after image processing succeeds.

The project is a single-user fork that is frequently rebased onto upstream. The design therefore keeps the current firmware protocol, data documents, endpoints, and non-authoritative workflow, and confines new behavior to a per-device policy and narrowly scoped delivery bookkeeping.

## Goals / Non-Goals

**Goals:**

- Enforce server ownership in backend code rather than relying only on a disabled UI control.
- Make a saved authoritative update recoverable after an unavailable device or partial direct push.
- Prevent an older delivery attempt from clearing pending state for a newer save.
- Preserve one-time enrollment from firmware and the existing opt-out workflow.
- Keep the implementation local and additive for straightforward upstream rebases.

**Non-Goals:**

- Prove durable application on firmware or introduce an acknowledgement protocol.
- Change firmware, firmware endpoints, or the `X-Config-Payload` schema.
- Make device-side application atomic.
- Repair general queue item consumption semantics.
- Add background workers, an outbox, delivery histories, retries independent of image fetches, or multi-user conflict resolution.

## Decisions

### 1. Add policy and pending flags to the existing device row

Add `server_authoritative` and `config_sync_pending` boolean columns. Both default to true in the migration, so existing and new devices adopt the selected single-user behavior. Existing JSON documents and `config_last_updated` remain unchanged.

The pending flag is delivery bookkeeping, not acknowledgement. It means no complete delivery attempt for the latest persisted generation has succeeded. Keeping it on the device row avoids a new table, jobs, and lifecycle management.

Alternative considered: introduce desired and applied revisions. That would imply an acknowledgement contract the current firmware cannot satisfy and would expand the fork unnecessarily.

### 2. Retain the timestamp as a server generation, not an authority signal

For authoritative devices, a server save advances `config_last_updated` strictly monotonically using a value greater than both the stored value and current Unix time. Device timestamps never cause a pull or modify this value in authoritative mode.

The save sets `config_sync_pending=true` in the same database transaction as config, processing, palette, generated access token, and any synchronized device-row fields handled by that request. Serialization and validation happen before the transaction. A transaction failure prevents direct delivery.

Delivery completion clears pending with a conditional update matching both device ID and the captured `config_last_updated`. If another save advances the generation during delivery, the older attempt cannot clear the newer pending update.

Alternative considered: use only a boolean. A boolean alone has a lost-update race when concurrent saves and delivery attempts overlap.

### 3. Centralize authoritative ownership checks

Authoritative behavior is enforced at every device-to-server entry point:

- Image request reconciliation does not launch a config pull when the firmware timestamp is newer.
- Hardware refresh updates only observed inventory and telemetry, including dimensions, board/display identity, firmware version, battery, host reachability, and other non-managed facts already represented by the model.
- Explicit setting import returns a conflict or validation response while authoritative mode is enabled.
- Initial enrollment may seed supported firmware values before the new device is persisted as authoritative. Server-only processing fields come from server defaults.

When authoritative mode is disabled, the existing timestamp reconciliation and explicit import behavior remain. Processing imports continue to merge firmware-supported keys into the stored document so `converter`, `autoMode`, `epdOptimizePreset`, and future server-only keys survive.

Alternative considered: disable only the web button. That would leave image requests, direct API calls, and background refreshes able to overwrite server state.

### 4. Keep direct push, but treat it as one best-effort attempt

After the database transaction commits, the server attempts the existing firmware endpoints for processing settings, palette, and config. Config is sent last because it can change URL, network, sleep, or connectivity behavior. Every required call must succeed before the captured generation is conditionally marked no longer pending.

If host discovery fails, any endpoint fails, or clearing pending fails, the authoritative desired state remains or is conservatively treated as pending. The API reports saved-but-pending rather than failing a save that already committed. It reports pushed only when all endpoint calls and the conditional pending update succeed.

The client receives one narrowly scoped palette update method matching the existing firmware palette endpoint. No unified firmware endpoint is introduced.

Alternative considered: disable direct push. That is simpler but removes useful immediate feedback while the device is awake. Pending bookkeeping safely contains partial delivery without changing firmware.

### 5. Pending authoritative state overrides timestamp reconciliation

On an image request from an authoritative device, `config_sync_pending=true` causes the server to attach the complete existing deferred payload regardless of the relative firmware timestamp. The server conditionally clears pending for the captured generation only after image selection and processing have succeeded and the response containing the payload has been produced.

Any earlier error, including queue load or processing failure, leaves pending unchanged. The next successful fetch retries the entire snapshot, repairing a partial direct push. The queue item removal policy itself is unchanged.

Because the current firmware has no acknowledgement, response production is the strongest available completion point. A disconnect or device-side persistence error after response production may go undetected until another server save or manual retry. UI language must call this `pushed`, not `synchronized` or `applied`.

Alternative considered: attach the payload to every image response. That improves convergence but repeatedly writes firmware NVS and increases response-header usage.

### 6. Keep UI and API semantics explicit

The device editor exposes `server_authoritative`, enabled by default. In authoritative mode, the setting-import action is disabled with explanatory text. Inventory refresh remains a separate action where the existing UI supports it.

Save responses distinguish `pushed` from `pending`. Device API responses expose the authoritative and pending flags so reloading the page preserves status. The UI does not claim durable acknowledgement.

Disabling authoritative mode clears or ignores authoritative pending bookkeeping and returns reconciliation decisions to the legacy timestamp path. Re-enabling it marks the current server snapshot pending so it is pushed rather than immediately replaced by device state.

## Risks / Trade-offs

- [A produced response is not a firmware acknowledgement] -> Use `pushed` terminology, retain best-effort semantics in the specification, and avoid an applied revision field.
- [A deferred payload may exceed firmware header limits] -> Preserve the current bounded protocol and avoid adding data to the payload; protocol sizing is explicitly outside this change.
- [Partial direct delivery temporarily leaves mixed firmware state] -> Keep the complete server snapshot pending and resend it on the next successful image fetch.
- [Firmware local edits are overwritten or ignored in authoritative mode] -> Make the policy visible, disable import, and allow users to opt out per device.
- [Default-on migration changes behavior for users relying on bidirectional sync] -> Preserve all stored data and provide an immediate per-device opt-out; this default is intentional for the single-user fork.
- [Concurrent delivery and save can race] -> Advance a monotonic generation on save and clear pending only with a generation-matching conditional update.
- [New code increases rebase conflicts in central handlers] -> Put ownership, generation, and delivery decisions in a small service where practical, leaving handlers as adapters.

## Migration Plan

1. Add the two boolean columns with default true without rewriting existing configuration documents or timestamps.
2. Deploy backend ownership enforcement and pending delivery before exposing the controls in the webapp.
3. On the first authoritative save, advance the existing timestamp generation and mark the snapshot pending.
4. Existing pending devices converge through direct delivery or their next successful image fetch.
5. Rollback can ignore or remove the additive columns; existing JSON and timestamp data remain compatible with the prior workflow. Any device explicitly switched to non-authoritative mode already follows the legacy path.
