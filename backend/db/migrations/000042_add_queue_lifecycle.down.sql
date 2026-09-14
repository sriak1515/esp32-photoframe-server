CREATE TABLE device_histories_new (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id INTEGER,
    image_id  INTEGER,
    served_at DATETIME,
    FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE CASCADE,
    FOREIGN KEY (image_id) REFERENCES images(id) ON DELETE SET NULL
);
INSERT INTO device_histories_new (id, device_id, image_id, served_at)
    SELECT id, device_id, image_id, served_at FROM device_histories;
DROP TABLE device_histories;
ALTER TABLE device_histories_new RENAME TO device_histories;
CREATE INDEX idx_device_histories_device_id ON device_histories(device_id);
CREATE INDEX idx_device_histories_device_served
    ON device_histories(device_id, served_at DESC);

CREATE TABLE device_image_queue_new (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id  INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    image_id   INTEGER NOT NULL REFERENCES images(id) ON DELETE CASCADE,
    position   INTEGER NOT NULL DEFAULT 0,
    source     TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(device_id, position)
);
INSERT INTO device_image_queue_new (id, device_id, image_id, position, source, created_at)
SELECT id,
       device_id,
       image_id,
       ROW_NUMBER() OVER (PARTITION BY device_id ORDER BY position, id) * 10,
       source,
       created_at
FROM device_image_queue;
DROP TABLE device_image_queue;
ALTER TABLE device_image_queue_new RENAME TO device_image_queue;
CREATE INDEX idx_device_queue_device_position
    ON device_image_queue(device_id, position);
CREATE INDEX idx_device_queue_device_image
    ON device_image_queue(device_id, image_id);
