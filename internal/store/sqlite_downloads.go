package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"downex/internal/model"
)

// CreateDownload assigns d.ID and persists d at the end of queue d.QueueID (0 = the
// main queue, then stored in d.QueueID). Zero CreatedAt and UpdatedAt are set to the
// current time; d is only modified once the insert has committed. IDs increase and are
// never reused, even after deletes. A queue that does not exist is ErrQueueNotFound.
func (s *SQLite) CreateDownload(ctx context.Context, d *model.Download) error {
	if err := validateDownload(d); err != nil {
		return fmt.Errorf("store: create download: %w", err)
	}
	now := canonicalTime(time.Now())
	rec := *d
	stampNew(&rec, now)
	queue := createQueueID(rec.QueueID)
	args, err := downloadArgs(&rec)
	if err != nil {
		return fmt.Errorf("store: create download: %w", err)
	}
	args = append(args, sql.Named("queue_id", int64(queue)))

	var id int64
	err = s.writeTx(ctx, func(tx *sql.Tx) error {
		if err := requireQueue(ctx, tx, queue); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, insertDownloadSQL, args...)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return fmt.Errorf("store: create download: %w", err)
	}
	d.ID = model.DownloadID(id)
	d.QueueID = queue
	stampNew(d, now)
	return nil
}

// UpdateDownload replaces the persisted record with d (matched by d.ID).
func (s *SQLite) UpdateDownload(ctx context.Context, d *model.Download) error {
	if err := validateDownload(d); err != nil {
		return fmt.Errorf("store: update download: %w", err)
	}
	args, err := downloadArgs(d)
	if err != nil {
		return fmt.Errorf("store: update download %d: %w", d.ID, err)
	}
	err = s.writeTx(ctx, func(tx *sql.Tx) error {
		return updateDownload(ctx, tx, d.ID, args)
	})
	if err != nil {
		return fmt.Errorf("store: update download: %w", err)
	}
	return nil
}

// GetDownload returns the persisted record for id.
func (s *SQLite) GetDownload(ctx context.Context, id model.DownloadID) (*model.Download, error) {
	if err := s.checkOpen(ctx); err != nil {
		return nil, err
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+downloadSelect+` FROM downloads WHERE id = ?`, int64(id))
	d, err := scanDownload(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: get download: download %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: get download %d: %w", id, err)
	}
	return &d, nil
}

// ListDownloads returns all records ordered by ID. The result is never nil.
func (s *SQLite) ListDownloads(ctx context.Context) ([]model.Download, error) {
	if err := s.checkOpen(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+downloadSelect+` FROM downloads ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: list downloads: %w", err)
	}
	defer rows.Close()

	dec := newListDecoder()
	raws := make([]rawDownload, 0, listChunk)
	for rows.Next() {
		raws = append(raws, rawDownload{})
		if err := raws[len(raws)-1].scan(rows); err != nil {
			dec.wg.Wait()
			return nil, fmt.Errorf("store: list downloads: %w", err)
		}
		if len(raws) == listChunk {
			dec.add(raws, false)
			raws = make([]rawDownload, 0, listChunk)
		}
	}
	if err := rows.Err(); err != nil {
		dec.wg.Wait()
		return nil, fmt.Errorf("store: list downloads: %w", err)
	}
	dec.add(raws, true)
	out, err := dec.wait()
	if err != nil {
		return nil, fmt.Errorf("store: list downloads: %w", err)
	}
	return out, nil
}

// DeleteDownload removes a record; its segments are removed by the foreign
// key's ON DELETE CASCADE in the same statement.
func (s *SQLite) DeleteDownload(ctx context.Context, id model.DownloadID) error {
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM downloads WHERE id = ?`, int64(id))
		if err != nil {
			return err
		}
		return requireOneRow(res, id)
	})
	if err != nil {
		return fmt.Errorf("store: delete download: %w", err)
	}
	return nil
}

// SaveCheckpoint updates the record and replaces all of its segments in one
// transaction (UPDATE downloads, DELETE segments, INSERT segments). Either the
// whole checkpoint commits durably or nothing changes.
func (s *SQLite) SaveCheckpoint(ctx context.Context, d *model.Download, segs []model.Segment) error {
	if err := validateDownload(d); err != nil {
		return fmt.Errorf("store: save checkpoint: %w", err)
	}
	if err := validateSegments(segs); err != nil {
		return fmt.Errorf("store: save checkpoint for download %d: %w", d.ID, err)
	}
	args, err := downloadArgs(d)
	if err != nil {
		return fmt.Errorf("store: save checkpoint for download %d: %w", d.ID, err)
	}
	err = s.writeTx(ctx, func(tx *sql.Tx) error {
		if err := updateDownload(ctx, tx, d.ID, args); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM segments WHERE download_id = ?`, int64(d.ID)); err != nil {
			return fmt.Errorf("delete segments: %w", err)
		}
		return insertSegments(ctx, tx, d.ID, segs)
	})
	if err != nil {
		return fmt.Errorf("store: save checkpoint: %w", err)
	}
	return nil
}

// LoadSegments returns the latest checkpoint of a download ordered by segment
// ID. The result is never nil. One statement reads both the download's
// existence and its segments, so the answer comes from a single snapshot.
func (s *SQLite) LoadSegments(ctx context.Context, id model.DownloadID) ([]model.Segment, error) {
	if err := s.checkOpen(ctx); err != nil {
		return nil, err
	}
	const q = `SELECT s.seg_id, s.start_offset, s.end_offset, s.durable
FROM downloads d LEFT JOIN segments s ON s.download_id = d.id
WHERE d.id = ? ORDER BY s.seg_id`
	rows, err := s.db.QueryContext(ctx, q, int64(id))
	if err != nil {
		return nil, fmt.Errorf("store: load segments for download %d: %w", id, err)
	}
	defer rows.Close()

	found := false
	out := []model.Segment{}
	for rows.Next() {
		found = true
		var segID, start, end, durable sql.NullInt64
		if err := rows.Scan(&segID, &start, &end, &durable); err != nil {
			return nil, fmt.Errorf("store: load segments for download %d: %w", id, err)
		}
		if !segID.Valid { // the download exists but has no segments
			continue
		}
		out = append(out, model.Segment{
			ID:      int(segID.Int64),
			Start:   start.Int64,
			End:     end.Int64,
			Durable: durable.Int64,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: load segments for download %d: %w", id, err)
	}
	if !found {
		return nil, fmt.Errorf("store: load segments: download %d: %w", id, ErrNotFound)
	}
	return out, nil
}

// SetFileCheck replaces (nil: clears) the stored file check of download id.
func (s *SQLite) SetFileCheck(ctx context.Context, id model.DownloadID, fc *model.FileCheck) error {
	v, err := encodeFileCheck(fc)
	if err != nil {
		return fmt.Errorf("store: set file check: %w", err)
	}
	err = s.writeTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, setFileCheckSQL, v, int64(id))
		if err != nil {
			return err
		}
		return requireOneRow(res, id)
	})
	if err != nil {
		return fmt.Errorf("store: set file check: %w", err)
	}
	return nil
}

// updateDownload replaces the row for id with args (from downloadArgs).
func updateDownload(ctx context.Context, tx *sql.Tx, id model.DownloadID, args []any) error {
	args = append(args[:len(args):len(args)], sql.Named("id", int64(id))) // never write into the caller's array
	res, err := tx.ExecContext(ctx, updateDownloadSQL, args...)
	if err != nil {
		return fmt.Errorf("update download %d: %w", id, err)
	}
	return requireOneRow(res, id)
}

// insertSegments writes segs for download id with one prepared statement.
func insertSegments(ctx context.Context, tx *sql.Tx, id model.DownloadID, segs []model.Segment) (err error) {
	if len(segs) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, insertSegmentSQL)
	if err != nil {
		return fmt.Errorf("prepare segment insert: %w", err)
	}
	defer func() {
		if cerr := stmt.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close segment insert: %w", cerr)
		}
	}()
	for _, sg := range segs {
		if _, err := stmt.ExecContext(ctx, int64(id), int64(sg.ID), sg.Start, sg.End, sg.Durable); err != nil {
			return fmt.Errorf("insert segment %d: %w", sg.ID, err)
		}
	}
	return nil
}

// requireOneRow maps "no row affected" to ErrNotFound.
func requireOneRow(res sql.Result, id model.DownloadID) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("download %d: %w", id, ErrNotFound)
	}
	return nil
}
