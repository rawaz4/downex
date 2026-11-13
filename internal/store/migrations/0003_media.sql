-- Migration 3: streaming-media downloads (docs/MEDIA-SPEC.md §6).
--
-- Encoding rules (owned by sqlite_codec.go):
--   * kind holds the integer value of model.Kind (0 = file, also for every row added
--     before this migration; 1 = media);
--   * media is the JSON encoding of model.Media without its checkpoint (format, tracks,
--     selection, duration, container, title, page URL, fragment totals); NULL when the
--     download has no media description (file downloads);
--   * media_checkpoint is the JSON encoding of model.MediaCheckpoint (per-track bitmaps
--     of durable fragments); NULL when there is none.

ALTER TABLE downloads ADD COLUMN kind INTEGER NOT NULL DEFAULT 0;
ALTER TABLE downloads ADD COLUMN media TEXT;
ALTER TABLE downloads ADD COLUMN media_checkpoint BLOB;
