## 1. Persist Authoritative State

- [x] 1.1 Add up/down database migrations for default-true `server_authoritative` and `config_sync_pending` device columns without modifying existing config JSON or timestamps.
- [x] 1.2 Add the authoritative and pending fields to the device model and API serialization, with defaults that also cover non-database test construction.
- [x] 1.3 Add focused persistence helpers for strictly monotonic server generations and generation-guarded pending-state clearing.

## 2. Enforce Ownership And Save Ordering

- [x] 2.1 Update authoritative configuration saves to validate and serialize all supplied config, processing, palette, token, and synchronized row values before committing them with the new generation and pending flag in one transaction.
- [x] 2.2 Make every persistence error fail the save before host discovery or firmware requests, and return saved-but-pending after post-commit delivery failures.
- [x] 2.3 Prevent authoritative image requests and background refreshes from importing managed config, processing, or palette regardless of the reported device timestamp.
- [x] 2.4 Separate inventory refresh from explicit setting import, reject or disable imports for authoritative devices, and preserve server-only processing keys during non-authoritative imports.
- [x] 2.5 Preserve initial enrollment seeding from firmware while filling server-only processing fields from server defaults before the device becomes authoritative.
- [x] 2.6 Handle mode transitions so disabling restores legacy reconciliation and enabling marks the current server snapshot pending for delivery.

## 3. Complete Best-Effort Delivery

- [x] 3.1 Add a PhotoFrame client method for the existing palette update endpoint.
- [x] 3.2 Push processing, palette, and config after commit, with config last, and clear pending only when every endpoint succeeds and the captured generation still matches.
- [x] 3.3 Include the complete existing deferred payload whenever an authoritative device is pending, independent of relative server and firmware timestamps.
- [x] 3.4 Clear deferred pending state only for the captured generation after a successful image response containing the payload; retain pending across source, queue-load, processing, and response-construction failures.
- [x] 3.5 Leave the non-authoritative timestamp reconciliation and firmware protocol behavior unchanged.

## 4. Update Device Settings UI

- [x] 4.1 Add a default-enabled “Server manages this device's settings” control and persist it through the device API.
- [x] 4.2 Separate inventory refresh from setting import in the UI and grey out import with explanatory text while authoritative mode is enabled.
- [x] 4.3 Replace synchronized/online claims with authoritative, pushed, and pending messaging based on persisted API state and save results.

## 5. Verify Behavior And Compatibility

- [x] 5.1 Add migration and model tests proving existing JSON/timestamps are preserved and existing/new devices default to authoritative mode.
- [x] 5.2 Add handler/service tests proving newer device timestamps, automatic pulls, and explicit refresh/import cannot overwrite authoritative config, processing, palette, or server-only processing keys.
- [x] 5.3 Add failure-injection tests proving database errors prevent device calls and successful persistence precedes all direct pushes.
- [x] 5.4 Add delivery tests for full direct success, unavailable devices, each partial endpoint failure including palette, generation races, and deferred retry.
- [x] 5.5 Add image and queue-path tests proving pre-response failures retain pending state and a successful response clears only the matching generation.
- [x] 5.6 Add compatibility tests proving non-authoritative devices retain legacy reconciliation and existing firmware payload/API shapes are unchanged.
- [x] 5.7 Add webapp tests for default checkbox state, disabled import, inventory refresh availability, mode transitions, and pushed-versus-pending messages.
- [x] 5.8 Run backend tests, webapp tests, formatting, and static checks required by the repository.
