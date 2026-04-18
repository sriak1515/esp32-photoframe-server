-- Auth: ListTokens, RevokeToken guard clauses, and GetOrGenerateDeviceToken all
-- filter api_keys by user_id. Without this index those scans grow linearly
-- with token count.
CREATE INDEX IF NOT EXISTS idx_api_keys_user_id ON api_keys(user_id);

-- Hot path during config save: GetOrGenerateDeviceToken looks up
-- (user_id, device_id) on every bind/re-bind. Composite with user_id leading
-- so it also serves the plain user_id filter when needed.
CREATE INDEX IF NOT EXISTS idx_api_keys_user_device ON api_keys(user_id, device_id);

-- Device identification: /image/:source handler falls back to matching by
-- X-Hostname / client IP via devices.host on every fetch.
CREATE INDEX IF NOT EXISTS idx_devices_host ON devices(host);

-- Sync dedup: Synology and Immich sync do
--   WHERE synology_photo_id = ? AND source = ?
--   WHERE immich_asset_id = ? AND source = ?
-- per photo per batch. Source-leading composites also serve the
-- COUNT-by-source and "delete all of source X" queries.
CREATE INDEX IF NOT EXISTS idx_images_source ON images(source);
CREATE INDEX IF NOT EXISTS idx_images_source_synology ON images(source, synology_photo_id);
CREATE INDEX IF NOT EXISTS idx_images_source_immich ON images(source, immich_asset_id);
