-- Migration 5: download queues, their schedules and post-queue actions (docs/M6-SPEC.md §1).
--
-- Encoding rules (owned by sqlite_queues.go):
--   * queues: booleans are 0/1; post_action holds the integer value of model.PostAction;
--     weekdays has bit 0 for Monday up to bit 6 for Sunday (0 = once on once_date,
--     "YYYY-MM-DD" or ''); start_minute/stop_minute are minutes after local midnight;
--     schedule_mark is the latest applied schedule event in Unix nanoseconds, NULL = none;
--   * queue 1 is the main queue: it always exists and runs by default;
--   * downloads.queue_id is the id of the download's queue (every row added before this
--     migration belongs to the main queue); downloads.queue_pos orders the downloads of
--     one queue (ascending, ties by id). Existing downloads keep their id order.
--     Both are written by CreateDownload and the queue operations only, never by
--     UpdateDownload or SaveCheckpoint.

CREATE TABLE queues (
    id               INTEGER PRIMARY KEY AUTOINCREMENT, -- IDs only ever increase
    name             TEXT    NOT NULL,
    running          INTEGER NOT NULL,
    max_concurrent   INTEGER NOT NULL,
    speed_limit      INTEGER NOT NULL,
    schedule_enabled INTEGER NOT NULL,
    start_minute     INTEGER NOT NULL,
    stop_enabled     INTEGER NOT NULL,
    stop_minute      INTEGER NOT NULL,
    weekdays         INTEGER NOT NULL,
    once_date        TEXT    NOT NULL,
    wake_computer    INTEGER NOT NULL,
    repeat_minutes   INTEGER NOT NULL,
    post_action      INTEGER NOT NULL,
    schedule_mark    INTEGER
) STRICT;

INSERT INTO queues (id, name, running, max_concurrent, speed_limit, schedule_enabled, start_minute,
    stop_enabled, stop_minute, weekdays, once_date, wake_computer, repeat_minutes, post_action, schedule_mark)
VALUES (1, 'Main queue', 1, 0, 0, 0, 0, 0, 0, 0, '', 0, 0, 0, NULL);

ALTER TABLE downloads ADD COLUMN queue_id INTEGER NOT NULL DEFAULT 1;
ALTER TABLE downloads ADD COLUMN queue_pos INTEGER NOT NULL DEFAULT 0;
UPDATE downloads SET queue_pos = id;
CREATE INDEX downloads_by_queue ON downloads (queue_id, queue_pos, id);
