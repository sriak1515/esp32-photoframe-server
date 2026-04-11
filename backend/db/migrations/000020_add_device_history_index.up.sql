CREATE INDEX IF NOT EXISTS idx_device_histories_device_served ON device_histories(device_id, served_at DESC);
