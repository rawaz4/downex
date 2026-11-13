-- Migration 2: requester hints and classification of a download (docs/DESKTOP-SPEC.md §1-2).
--
-- Encoding rules (owned by sqlite_codec.go):
--   * source holds the integer value of model.Source (0 = unknown, rows added before
--     this migration);
--   * expected_size is the requester's size hint in bytes, 0 when unknown;
--   * mime is the requester's normalised MIME hint, '' when none;
--   * category is the name of the category chosen at Add, '' when none.
-- The new state value model.StateAwaitingConfirmation needs no schema change.

ALTER TABLE downloads ADD COLUMN source INTEGER NOT NULL DEFAULT 0;
ALTER TABLE downloads ADD COLUMN expected_size INTEGER NOT NULL DEFAULT 0;
ALTER TABLE downloads ADD COLUMN mime TEXT NOT NULL DEFAULT '';
ALTER TABLE downloads ADD COLUMN category TEXT NOT NULL DEFAULT '';
