-- Migration 4: the download complete dialog (docs/DESKTOP-SPEC.md §8).
--
-- Encoding rules (owned by sqlite_codec.go):
--   * completion_dialog is 1 once the user opened the download's progress window (its
--     completion or failure opens the dialog), else 0 (also for every row added before
--     this migration);
--   * started_at is when the download first started running, in Unix nanoseconds; NULL
--     when it never ran or the start was not recorded (rows added before this migration);
--   * active_ms is the total running time of all of its runs in milliseconds (pauses
--     excluded), 0 when unknown.

ALTER TABLE downloads ADD COLUMN completion_dialog INTEGER NOT NULL DEFAULT 0;
ALTER TABLE downloads ADD COLUMN started_at INTEGER;
ALTER TABLE downloads ADD COLUMN active_ms INTEGER NOT NULL DEFAULT 0;
