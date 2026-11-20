-- Migration 10: rescue of a partial file a browser left unfinished (docs/ENGINE-SPEC.md §7.4,
-- docs/DESKTOP-SPEC.md §11).
--
-- Encoding rules (owned by sqlite_codec.go):
--   * rescued_bytes is how many bytes of the download were adopted from another program's
--     partial file (0 ≤ rescued_bytes ≤ size when the size is known); 0 when none, after a
--     restart, and for every row added before this migration.

ALTER TABLE downloads ADD COLUMN rescued_bytes INTEGER NOT NULL DEFAULT 0;
