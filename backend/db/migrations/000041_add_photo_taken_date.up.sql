ALTER TABLE images ADD COLUMN photo_taken_date TEXT;
UPDATE images
SET photo_taken_date = substr(photo_taken_at, 1, 10)
WHERE photo_taken_at IS NOT NULL
  AND date(photo_taken_at) IS NOT NULL;
CREATE INDEX idx_images_photo_taken_date ON images(photo_taken_date);
