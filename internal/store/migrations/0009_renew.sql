-- Migration 9: expired links (docs/ENGINE-SPEC.md §7.2-7.3, docs/DESKTOP-SPEC.md §10).
--
-- Encoding rules (owned by sqlite_codec.go):
--   * page_url is the page a file download was started from (an http or https URL of at
--     most 32 KiB); '' when unknown (also for every row added before this migration);
--   * remote_name is the server's name for the file at the first probe, before any
--     "name (n).ext" variant; '' before the probe, after a restart and for older rows.

ALTER TABLE downloads ADD COLUMN page_url TEXT NOT NULL DEFAULT '';
ALTER TABLE downloads ADD COLUMN remote_name TEXT NOT NULL DEFAULT '';
