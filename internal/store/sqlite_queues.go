package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"downex/internal/model"
)

// This file owns the mapping between model.Queue and the queues table of
// migrations/0005_queues.sql, and the queue membership columns of downloads
// (queue_id, queue_pos).

var _ QueueStore = (*SQLite)(nil)

const (
	queueColumns = `id, name, running, max_concurrent, speed_limit, schedule_enabled, start_minute,
	stop_enabled, stop_minute, weekdays, once_date, wake_computer, repeat_minutes, post_action, schedule_mark`

	insertQueueSQL = `INSERT INTO queues (name, running, max_concurrent, speed_limit, schedule_enabled,
	start_minute, stop_enabled, stop_minute, weekdays, once_date, wake_computer, repeat_minutes, post_action,
	schedule_mark)
VALUES (:name, :running, :max_concurrent, :speed_limit, :schedule_enabled, :start_minute, :stop_enabled,
	:stop_minute, :weekdays, :once_date, :wake_computer, :repeat_minutes, :post_action, :schedule_mark)`

	updateQueueSQL = `UPDATE queues SET name = :name, running = :running, max_concurrent = :max_concurrent,
	speed_limit = :speed_limit, schedule_enabled = :schedule_enabled, start_minute = :start_minute,
	stop_enabled = :stop_enabled, stop_minute = :stop_minute, weekdays = :weekdays, once_date = :once_date,
	wake_computer = :wake_computer, repeat_minutes = :repeat_minutes, post_action = :post_action,
	schedule_mark = :schedule_mark
WHERE id = :id`
)

// createQueueID is the queue a new download goes into: the main queue for 0.
func createQueueID(id model.QueueID) model.QueueID {
	if id == 0 {
		return model.MainQueueID
	}
	return id
}

// queueArgs encodes every persisted field of q except its ID and downloads.
func queueArgs(q *model.Queue) []any {
	s := q.Schedule
	return []any{
		sql.Named("name", q.Name),
		sql.Named("running", q.Running),
		sql.Named("max_concurrent", int64(q.MaxConcurrent)),
		sql.Named("speed_limit", q.SpeedLimit),
		sql.Named("schedule_enabled", s.Enabled),
		sql.Named("start_minute", int64(s.StartMinute)),
		sql.Named("stop_enabled", s.StopEnabled),
		sql.Named("stop_minute", int64(s.StopMinute)),
		sql.Named("weekdays", int64(s.Weekdays)),
		sql.Named("once_date", s.OnceDate),
		sql.Named("wake_computer", s.WakeComputer),
		sql.Named("repeat_minutes", int64(s.RepeatMinutes)),
		sql.Named("post_action", int64(q.PostAction)),
		sql.Named("schedule_mark", encodeTime(q.ScheduleMark)),
	}
}

// ListQueues returns every queue ordered by ID with its downloads in queue order. One
// statement reads both, so the answer comes from a single snapshot.
func (s *SQLite) ListQueues(ctx context.Context) ([]model.Queue, error) {
	if err := s.checkOpen(ctx); err != nil {
		return nil, err
	}
	const q = `SELECT q.id, q.name, q.running, q.max_concurrent, q.speed_limit, q.schedule_enabled,
	q.start_minute, q.stop_enabled, q.stop_minute, q.weekdays, q.once_date, q.wake_computer, q.repeat_minutes,
	q.post_action, q.schedule_mark, d.id
FROM queues q LEFT JOIN downloads d ON d.queue_id = q.id
ORDER BY q.id, d.queue_pos, d.id`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("store: list queues: %w", err)
	}
	defer rows.Close()

	out := []model.Queue{}
	for rows.Next() {
		var (
			qu         model.Queue
			id, maxC   int64
			start, stp int64
			days, rep  int64
			action     int64
			mark       sql.NullInt64
			download   sql.NullInt64
		)
		err := rows.Scan(&id, &qu.Name, &qu.Running, &maxC, &qu.SpeedLimit, &qu.Schedule.Enabled, &start,
			&qu.Schedule.StopEnabled, &stp, &days, &qu.Schedule.OnceDate, &qu.Schedule.WakeComputer, &rep,
			&action, &mark, &download)
		if err != nil {
			return nil, fmt.Errorf("store: list queues: %w", err)
		}
		if n := len(out); n == 0 || out[n-1].ID != model.QueueID(id) {
			qu.ID = model.QueueID(id)
			qu.MaxConcurrent = int(maxC)
			qu.Schedule.StartMinute, qu.Schedule.StopMinute = int(start), int(stp)
			qu.Schedule.Weekdays, qu.Schedule.RepeatMinutes = int(days), int(rep)
			qu.PostAction = model.PostAction(action)
			qu.ScheduleMark = decodeTime(mark)
			qu.DownloadIDs = []model.DownloadID{}
			out = append(out, qu)
		}
		if download.Valid {
			last := &out[len(out)-1]
			last.DownloadIDs = append(last.DownloadIDs, model.DownloadID(download.Int64))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list queues: %w", err)
	}
	return out, nil
}

// CreateQueue assigns q.ID and persists q (without downloads).
func (s *SQLite) CreateQueue(ctx context.Context, q *model.Queue) error {
	if err := validateQueue(q); err != nil {
		return fmt.Errorf("store: create queue: %w", err)
	}
	var id int64
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, insertQueueSQL, queueArgs(q)...)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return fmt.Errorf("store: create queue: %w", err)
	}
	q.ID = model.QueueID(id)
	return nil
}

// UpdateQueue replaces the persisted queue q.ID with q (its downloads are kept).
func (s *SQLite) UpdateQueue(ctx context.Context, q *model.Queue) error {
	if err := validateQueue(q); err != nil {
		return fmt.Errorf("store: update queue: %w", err)
	}
	args := append(queueArgs(q), sql.Named("id", int64(q.ID)))
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, updateQueueSQL, args...)
		if err != nil {
			return err
		}
		return requireOneQueue(res, q.ID)
	})
	if err != nil {
		return fmt.Errorf("store: update queue: %w", err)
	}
	return nil
}

// DeleteQueue removes queue id and appends its downloads, in their order, to the main
// queue.
func (s *SQLite) DeleteQueue(ctx context.Context, id model.QueueID) error {
	if id == model.MainQueueID {
		return fmt.Errorf("store: delete queue: %w: the main queue cannot be deleted", ErrInvalid)
	}
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		if err := requireQueue(ctx, tx, id); err != nil {
			return err
		}
		// Shifted past the last position of the main queue, keeping their order.
		var first, last int64
		err := tx.QueryRowContext(ctx, `SELECT COALESCE(MIN(queue_pos), 0) FROM downloads WHERE queue_id = ?`,
			int64(id)).Scan(&first)
		if err == nil {
			err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(queue_pos), 0) FROM downloads WHERE queue_id = ?`,
				int64(model.MainQueueID)).Scan(&last)
		}
		if err != nil {
			return fmt.Errorf("read queue positions: %w", err)
		}
		const move = `UPDATE downloads SET queue_id = ?, queue_pos = queue_pos + ? WHERE queue_id = ?`
		if _, err := tx.ExecContext(ctx, move, int64(model.MainQueueID), last-first+1, int64(id)); err != nil {
			return fmt.Errorf("move downloads to the main queue: %w", err)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM queues WHERE id = ?`, int64(id))
		if err != nil {
			return err
		}
		return requireOneQueue(res, id)
	})
	if err != nil {
		return fmt.Errorf("store: delete queue: %w", err)
	}
	return nil
}

// MoveDownloads moves ids into queue to at position and renumbers that queue's order.
func (s *SQLite) MoveDownloads(ctx context.Context, ids []model.DownloadID, to model.QueueID, position int) error {
	moved := model.UniqueDownloadIDs(ids)
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		if err := requireQueue(ctx, tx, to); err != nil {
			return err
		}
		for _, id := range moved {
			var one int
			err := tx.QueryRowContext(ctx, `SELECT 1 FROM downloads WHERE id = ?`, int64(id)).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("download %d: %w", id, ErrNotFound)
			}
			if err != nil {
				return fmt.Errorf("read download %d: %w", id, err)
			}
		}
		current, err := queueOrder(ctx, tx, to)
		if err != nil {
			return err
		}
		order := model.PlaceDownloads(current, moved, position)
		stmt, err := tx.PrepareContext(ctx, `UPDATE downloads SET queue_id = ?, queue_pos = ? WHERE id = ?`)
		if err != nil {
			return fmt.Errorf("prepare queue order: %w", err)
		}
		defer stmt.Close()
		for i, id := range order {
			if _, err := stmt.ExecContext(ctx, int64(to), int64(i+1), int64(id)); err != nil {
				return fmt.Errorf("place download %d: %w", id, err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("store: move downloads: %w", err)
	}
	return nil
}

// queueOrder reads the downloads of queue id in queue order.
func queueOrder(ctx context.Context, tx *sql.Tx, id model.QueueID) ([]model.DownloadID, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM downloads WHERE queue_id = ? ORDER BY queue_pos, id`, int64(id))
	if err != nil {
		return nil, fmt.Errorf("read queue %d: %w", id, err)
	}
	defer rows.Close()
	var out []model.DownloadID
	for rows.Next() {
		var d int64
		if err := rows.Scan(&d); err != nil {
			return nil, fmt.Errorf("read queue %d: %w", id, err)
		}
		out = append(out, model.DownloadID(d))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read queue %d: %w", id, err)
	}
	return out, nil
}

// requireQueue fails with ErrQueueNotFound unless queue id exists.
func requireQueue(ctx context.Context, tx *sql.Tx, id model.QueueID) error {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM queues WHERE id = ?`, int64(id)).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("queue %d: %w", id, ErrQueueNotFound)
	case err != nil:
		return fmt.Errorf("read queue %d: %w", id, err)
	}
	return nil
}

// requireOneQueue maps "no row affected" to ErrQueueNotFound.
func requireOneQueue(res sql.Result, id model.QueueID) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("queue %d: %w", id, ErrQueueNotFound)
	}
	return nil
}
