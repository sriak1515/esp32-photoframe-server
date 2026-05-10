-- Procedural / generative image sources (fractal zoom, DLA growth, …) live
-- under their own `source` values in the existing image-source dispatch.
-- They produce images on demand rather than syncing into the images table,
-- so we don't insert rows here — we just need a small per-device, per-source
-- state blob the generator round-trips itself. Size is ~80 bytes for fractal
-- and ~250 KB for DLA at 800×480, well within SQLite BLOB capacity.

CREATE TABLE IF NOT EXISTS generative_states (
    device_id INTEGER NOT NULL,
    source TEXT NOT NULL,
    state BLOB,
    updated_at DATETIME,
    PRIMARY KEY (device_id, source)
);
