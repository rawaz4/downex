-- Migration 7: downloading a completed file again in place (docs/ENGINE-SPEC.md §7.1).
--
-- Encoding rules (owned by sqlite_codec.go):
--   * replace_file is 1 while a completed download is downloaded again to replace its file
--     (the file under filename stays until the new copy is complete), else 0 (also for
--     every row added before this migration).

ALTER TABLE downloads ADD COLUMN replace_file INTEGER NOT NULL DEFAULT 0;
