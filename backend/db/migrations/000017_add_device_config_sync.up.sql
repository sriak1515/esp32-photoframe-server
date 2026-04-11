ALTER TABLE devices ADD COLUMN device_config TEXT DEFAULT '{}';
ALTER TABLE devices ADD COLUMN device_processing_settings TEXT DEFAULT '{}';
ALTER TABLE devices ADD COLUMN device_color_palette TEXT DEFAULT '{}';
ALTER TABLE devices ADD COLUMN config_last_updated BIGINT DEFAULT 0;
