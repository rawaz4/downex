-- Migration 8: the file check of downloaded files (docs/DESKTOP-SPEC.md §9).
--
-- Encoding rules (owned by sqlite_codec.go):
--   * served_type is the media type of the probe response's Content-Type, lower case,
--     without parameters; '' when none (also for every row added before this migration);
--   * name_controls is 1 when the name the server or requester proposed held bidi
--     overrides, embeddings or isolates that sanitising removed, else 0;
--   * file_check is the JSON encoding of model.FileCheck (the final check of a completed
--     download); NULL when there is none. Only SetFileCheck writes it: inserts store NULL
--     and updates leave it as it is. Invalid JSON reads as none (the check is advisory).

ALTER TABLE downloads ADD COLUMN served_type TEXT NOT NULL DEFAULT '';
ALTER TABLE downloads ADD COLUMN name_controls INTEGER NOT NULL DEFAULT 0;
ALTER TABLE downloads ADD COLUMN file_check TEXT;
