UPDATE images SET source = 'telegram' WHERE source = 'gallery';
UPDATE settings SET value = 'telegram' WHERE key = 'image_source' AND value = 'gallery';
