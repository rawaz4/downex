-- Migration 1: downloads and their segment checkpoints (docs/ENGINE-SPEC.md §1, §6–7).
--
-- Encoding rules (owned by sqlite_codec.go):
--   * timestamps are Unix nanoseconds; NULL stands for the zero time;
--   * headers is a JSON object (header name -> list of values); NULL when the
--     request carries no header map;
--   * body is the raw request body; NULL when the request has no body;
--   * mode and state hold the integer values of model.Mode and model.State.

CREATE TABLE downloads (
    id              INTEGER PRIMARY KEY AUTOINCREMENT, -- IDs only ever increase
    url             TEXT    NOT NULL,
    method          TEXT    NOT NULL,
    headers         TEXT,
    body            BLOB,
    final_url       TEXT    NOT NULL,
    dir             TEXT    NOT NULL,
    filename        TEXT    NOT NULL,
    size            INTEGER NOT NULL, -- -1 when unknown
    mode            INTEGER NOT NULL,
    state           INTEGER NOT NULL,
    etag            TEXT    NOT NULL,
    etag_strong     INTEGER NOT NULL,
    last_modified   TEXT    NOT NULL,
    max_connections INTEGER NOT NULL,
    speed_limit     INTEGER NOT NULL,
    error           TEXT    NOT NULL,
    created_at      INTEGER,
    updated_at      INTEGER,
    completed_at    INTEGER
) STRICT;

-- One row per segment of the latest checkpoint. Ranges are inclusive;
-- end_offset = -1 marks an open-ended segment (single mode only).
CREATE TABLE segments (
    download_id  INTEGER NOT NULL REFERENCES downloads (id) ON DELETE CASCADE,
    seg_id       INTEGER NOT NULL,
    start_offset INTEGER NOT NULL,
    end_offset   INTEGER NOT NULL,
    durable      INTEGER NOT NULL,
    PRIMARY KEY (download_id, seg_id)
) STRICT, WITHOUT ROWID;
