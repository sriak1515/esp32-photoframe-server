CREATE TABLE device_image_queue (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id   INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    image_id    INTEGER NOT NULL REFERENCES images(id) ON DELETE CASCADE,
    position    INTEGER NOT NULL DEFAULT 0,
    source      TEXT    NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(device_id, position)
);

CREATE INDEX idx_device_queue_device_position
    ON device_image_queue(device_id, position);
CREATE INDEX idx_device_queue_device_image
    ON device_image_queue(device_id, image_id);
