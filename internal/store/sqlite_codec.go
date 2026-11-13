package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"downex/internal/model"
)

// This file owns the mapping between model.Download / model.Segment and the
// columns created by migrations/0001_init.sql, 0002_desktop.sql, 0003_media.sql, 0004_completion.sql
// the downloads columns of 0005_queues.sql (sqlite_queues.go owns the queues table) and
// 0007_replace.sql. Named parameters tie every
// value to its column, so the column order of a statement cannot drift.

// downloadSelect lists the columns scanned by scanDownload, in order.
const downloadSelect = `id, url, method, headers, body IS NOT NULL, body, final_url, dir, filename,
	size, mode, state, etag, etag_strong, last_modified, max_connections, speed_limit, error,
	created_at, updated_at, completed_at, source, expected_size, mime, category, kind, media, media_checkpoint,
	completion_dialog, started_at, active_ms, queue_id, replace_file`

const (
	insertDownloadSQL = `INSERT INTO downloads (url, method, headers, body, final_url, dir, filename,
	size, mode, state, etag, etag_strong, last_modified, max_connections, speed_limit, error,
	created_at, updated_at, completed_at, source, expected_size, mime, category, kind, media, media_checkpoint,
	completion_dialog, started_at, active_ms, queue_id, queue_pos, replace_file)
VALUES (:url, :method, :headers, :body, :final_url, :dir, :filename,
	:size, :mode, :state, :etag, :etag_strong, :last_modified, :max_connections, :speed_limit, :error,
	:created_at, :updated_at, :completed_at, :source, :expected_size, :mime, :category, :kind, :media,
	:media_checkpoint, :completion_dialog, :started_at, :active_ms, :queue_id,
	(SELECT COALESCE(MAX(queue_pos), 0) + 1 FROM downloads WHERE queue_id = :queue_id), :replace_file)`

	updateDownloadSQL = `UPDATE downloads SET url = :url, method = :method, headers = :headers,
	body = :body, final_url = :final_url, dir = :dir, filename = :filename, size = :size,
	mode = :mode, state = :state, etag = :etag, etag_strong = :etag_strong,
	last_modified = :last_modified, max_connections = :max_connections,
	speed_limit = :speed_limit, error = :error, created_at = :created_at,
	updated_at = :updated_at, completed_at = :completed_at, source = :source,
	expected_size = :expected_size, mime = :mime, category = :category, kind = :kind, media = :media,
	media_checkpoint = :media_checkpoint, completion_dialog = :completion_dialog, started_at = :started_at,
	active_ms = :active_ms, replace_file = :replace_file
WHERE id = :id`

	insertSegmentSQL = `INSERT INTO segments (download_id, seg_id, start_offset, end_offset, durable)
VALUES (?, ?, ?, ?, ?)`
)

// downloadArgs encodes every persisted field of d except its ID and its queue
// membership as named parameters for insertDownloadSQL (which also takes :queue_id)
// and updateDownloadSQL; the driver rejects a statement whose named parameter has no
// argument.
func downloadArgs(d *model.Download) ([]any, error) {
	headers, err := encodeHeaders(d.Request.Headers)
	if err != nil {
		return nil, err
	}
	media, checkpoint, err := encodeMedia(d.Media)
	if err != nil {
		return nil, err
	}
	return []any{
		sql.Named("url", d.Request.URL),
		sql.Named("method", d.Request.Method),
		sql.Named("headers", headers),
		// The driver binds a nil body as NULL and an empty one as a zero-length
		// blob; the NilAndEmptyPreserved conformance test pins this down.
		sql.Named("body", d.Request.Body),
		sql.Named("final_url", d.FinalURL),
		sql.Named("dir", d.Dir),
		sql.Named("filename", d.Filename),
		sql.Named("size", d.Size),
		sql.Named("mode", int64(d.Mode)),
		sql.Named("state", int64(d.State)),
		sql.Named("etag", d.Validators.ETag),
		sql.Named("etag_strong", d.Validators.StrongETag),
		sql.Named("last_modified", d.Validators.LastModified),
		sql.Named("max_connections", int64(d.MaxConnections)),
		sql.Named("speed_limit", d.SpeedLimit),
		sql.Named("error", d.Error),
		sql.Named("created_at", encodeTime(d.CreatedAt)),
		sql.Named("updated_at", encodeTime(d.UpdatedAt)),
		sql.Named("completed_at", encodeTime(d.CompletedAt)),
		sql.Named("source", int64(d.Source)),
		sql.Named("expected_size", d.ExpectedSize),
		sql.Named("mime", d.MIME),
		sql.Named("category", d.Category),
		sql.Named("kind", int64(d.Kind)),
		sql.Named("media", media),
		sql.Named("media_checkpoint", checkpoint),
		sql.Named("completion_dialog", d.CompletionDialog),
		sql.Named("started_at", encodeTime(d.StartedAt)),
		sql.Named("active_ms", d.ActiveMS),
		sql.Named("replace_file", d.ReplaceFile),
	}, nil
}

// rowScanner is implemented by *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanDownload decodes one row selected with downloadSelect.
func scanDownload(r rowScanner) (model.Download, error) {
	var (
		d                                 model.Download
		id, mode, state, maxConns, source int64
		kind, queueID                     int64
		headers, media                    sql.NullString
		checkpoint                        []byte
		hasBody                           bool
		body                              []byte
		createdAt, updatedAt, completedAt sql.NullInt64
		startedAt                         sql.NullInt64
	)
	err := r.Scan(&id, &d.Request.URL, &d.Request.Method, &headers, &hasBody, &body,
		&d.FinalURL, &d.Dir, &d.Filename, &d.Size, &mode, &state,
		&d.Validators.ETag, &d.Validators.StrongETag, &d.Validators.LastModified,
		&maxConns, &d.SpeedLimit, &d.Error, &createdAt, &updatedAt, &completedAt,
		&source, &d.ExpectedSize, &d.MIME, &d.Category, &kind, &media, &checkpoint,
		&d.CompletionDialog, &startedAt, &d.ActiveMS, &queueID, &d.ReplaceFile)
	if err != nil {
		return model.Download{}, err
	}
	d.ID = model.DownloadID(id)
	if d.Request.Headers, err = decodeHeaders(headers); err != nil {
		return model.Download{}, fmt.Errorf("download %d: %w", id, err)
	}
	d.Request.Body = decodeBody(hasBody, body)
	d.Mode = model.Mode(mode)
	d.State = model.State(state)
	d.MaxConnections = int(maxConns)
	d.Source = model.Source(source)
	d.Kind = model.Kind(kind)
	d.QueueID = model.QueueID(queueID)
	if d.Media, err = decodeMedia(media, checkpoint); err != nil {
		return model.Download{}, fmt.Errorf("download %d: %w", id, err)
	}
	d.CreatedAt = decodeTime(createdAt)
	d.UpdatedAt = decodeTime(updatedAt)
	d.CompletedAt = decodeTime(completedAt)
	d.StartedAt = decodeTime(startedAt)
	return d, nil
}

// encodeHeaders stores a header map as JSON, keeping nil distinct from empty.
func encodeHeaders(h http.Header) (sql.NullString, error) {
	if h == nil {
		return sql.NullString{}, nil
	}
	b, err := json.Marshal(h)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("encode request headers: %w", err)
	}
	return sql.NullString{String: string(b), Valid: true}, nil
}

// encodeMedia stores a media description as JSON (NULL for none) and its checkpoint
// as a separate JSON blob (NULL for none).
func encodeMedia(m *model.Media) (media sql.NullString, checkpoint []byte, err error) {
	if m == nil {
		return sql.NullString{}, nil, nil
	}
	b, err := json.Marshal(m) // without the checkpoint (json:"-")
	if err != nil {
		return sql.NullString{}, nil, fmt.Errorf("encode media: %w", err)
	}
	if m.Checkpoint != nil {
		if checkpoint, err = json.Marshal(m.Checkpoint); err != nil {
			return sql.NullString{}, nil, fmt.Errorf("encode media checkpoint: %w", err)
		}
	}
	return sql.NullString{String: string(b), Valid: true}, checkpoint, nil
}

// decodeMedia parses the stored media columns. The driver reads a NULL blob as nil;
// encodeMedia never writes an empty one.
func decodeMedia(media sql.NullString, checkpoint []byte) (*model.Media, error) {
	if !media.Valid {
		if checkpoint != nil {
			return nil, fmt.Errorf("decode media: a checkpoint is stored without a media description")
		}
		return nil, nil
	}
	var m model.Media
	if err := json.Unmarshal([]byte(media.String), &m); err != nil {
		return nil, fmt.Errorf("decode media: %w", err)
	}
	if checkpoint != nil {
		var cp model.MediaCheckpoint
		if err := json.Unmarshal(checkpoint, &cp); err != nil {
			return nil, fmt.Errorf("decode media checkpoint: %w", err)
		}
		m.Checkpoint = &cp
	}
	return &m, nil
}

// decodeHeaders parses a stored header column.
func decodeHeaders(s sql.NullString) (http.Header, error) {
	if !s.Valid {
		return nil, nil
	}
	var h http.Header
	if err := json.Unmarshal([]byte(s.String), &h); err != nil {
		return nil, fmt.Errorf("decode request headers: %w", err)
	}
	if h == nil { // a stored JSON null; the store never writes one
		h = http.Header{}
	}
	return h, nil
}

// decodeBody restores the request body. The driver reads a zero-length blob
// as nil, so the separate "body IS NOT NULL" column keeps empty distinct from absent.
func decodeBody(present bool, b []byte) []byte {
	if !present {
		return nil
	}
	if b == nil {
		return []byte{}
	}
	return b
}

// encodeTime stores t as Unix nanoseconds; the zero time is stored as NULL.
// validateDownload has already rejected times outside the int64 range.
func encodeTime(t time.Time) sql.NullInt64 {
	if t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UnixNano(), Valid: true}
}

// decodeTime is the inverse of encodeTime; times are returned in UTC.
func decodeTime(n sql.NullInt64) time.Time {
	if !n.Valid {
		return time.Time{}
	}
	return time.Unix(0, n.Int64).UTC()
}
