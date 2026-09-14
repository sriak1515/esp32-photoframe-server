CREATE TABLE device_image_queue_new (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id        INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    image_id         INTEGER NOT NULL REFERENCES images(id) ON DELETE RESTRICT,
    position         INTEGER NOT NULL,
    source           TEXT NOT NULL DEFAULT '',
    state            TEXT NOT NULL DEFAULT 'pending'
                     CHECK (state IN ('pending', 'claimed', 'leased', 'failed', 'permanently_invalid', 'delivered')),
    claim_expires_at DATETIME,
    lease_expires_at DATETIME,
    attempt_count    INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at  DATETIME,
    last_attempt_at  DATETIME,
    last_error_code  TEXT,
    last_error       TEXT,
    created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(device_id, position)
);

INSERT INTO device_image_queue_new (
    id, device_id, image_id, position, source, state, attempt_count, created_at
)
SELECT id,
       device_id,
       image_id,
       ROW_NUMBER() OVER (PARTITION BY device_id ORDER BY position, id) * 10,
       source,
       'pending',
       0,
       created_at
FROM device_image_queue;

DROP TABLE device_image_queue;
ALTER TABLE device_image_queue_new RENAME TO device_image_queue;
CREATE INDEX idx_device_queue_device_position
    ON device_image_queue(device_id, position);
CREATE INDEX idx_device_queue_device_image
    ON device_image_queue(device_id, image_id);

CREATE TABLE device_histories_new (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id     INTEGER,
    image_id      INTEGER,
    queue_item_id INTEGER,
    served_at     DATETIME,
    FOREIGN KEY (device_id) REFERENCES devices(id) ON DELETE CASCADE,
    FOREIGN KEY (image_id) REFERENCES images(id) ON DELETE SET NULL
);
INSERT INTO device_histories_new (id, device_id, image_id, queue_item_id, served_at)
    SELECT id, device_id, image_id, NULL, served_at FROM device_histories;
DROP TABLE device_histories;
ALTER TABLE device_histories_new RENAME TO device_histories;
CREATE INDEX idx_device_histories_device_id ON device_histories(device_id);
CREATE INDEX idx_device_histories_device_served
    ON device_histories(device_id, served_at DESC);
CREATE UNIQUE INDEX idx_device_histories_queue_item_id
    ON device_histories(queue_item_id)
    WHERE queue_item_id IS NOT NULL;
