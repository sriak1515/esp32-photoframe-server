-- Rename the "telegram" image source to "gallery". Telegram is no longer a
-- distinct source — the bot now uploads into the standalone gallery, and any
-- device whose source was set to telegram should follow the same images.
UPDATE images SET source = 'gallery' WHERE source = 'telegram';
UPDATE settings SET value = 'gallery' WHERE key = 'image_source' AND value = 'telegram';
