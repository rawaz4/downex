package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"sync"
	"time"

	"downex/internal/model"
)

// This file owns the mapping between model.Download / model.Segment and the
// columns created by migrations/0001_init.sql, 0002_desktop.sql, 0003_media.sql, 0004_completion.sql
// the downloads columns of 0005_queues.sql (sqlite_queues.go owns the queues table) and
// 0007_replace.sql, 0008_filecheck.sql, 0009_renew.sql and 0010_rescue.sql. Named parameters tie every
// value to its column, so the column order of a statement cannot drift.

// downloadSelect lists the columns scanned by scanDownload, in order.
const downloadSelect = `id, url, method, headers, body IS NOT NULL, body, final_url, dir, filename,
	size, mode, state, etag, etag_strong, last_modified, max_connections, speed_limit, error,
	created_at, updated_at, completed_at, source, expected_size, mime, category, kind, media, media_checkpoint,
	completion_dialog, started_at, active_ms, queue_id, replace_file, served_type, name_controls, file_check,
	page_url, remote_name, rescued_bytes`

const (
	// file_check is inserted as NULL and never updated here: only SetFileCheck writes it.
	insertDownloadSQL = `INSERT INTO downloads (url, method, headers, body, final_url, dir, filename,
	size, mode, state, etag, etag_strong, last_modified, max_connections, speed_limit, error,
	created_at, updated_at, completed_at, source, expected_size, mime, category, kind, media, media_checkpoint,
	completion_dialog, started_at, active_ms, queue_id, queue_pos, replace_file, served_type, name_controls,
	file_check, page_url, remote_name, rescued_bytes)
VALUES (:url, :method, :headers, :body, :final_url, :dir, :filename,
	:size, :mode, :state, :etag, :etag_strong, :last_modified, :max_connections, :speed_limit, :error,
	:created_at, :updated_at, :completed_at, :source, :expected_size, :mime, :category, :kind, :media,
	:media_checkpoint, :completion_dialog, :started_at, :active_ms, :queue_id,
	(SELECT COALESCE(MAX(queue_pos), 0) + 1 FROM downloads WHERE queue_id = :queue_id), :replace_file,
	:served_type, :name_controls, NULL, :page_url, :remote_name, :rescued_bytes)`

	updateDownloadSQL = `UPDATE downloads SET url = :url, method = :method, headers = :headers,
	body = :body, final_url = :final_url, dir = :dir, filename = :filename, size = :size,
	mode = :mode, state = :state, etag = :etag, etag_strong = :etag_strong,
	last_modified = :last_modified, max_connections = :max_connections,
	speed_limit = :speed_limit, error = :error, created_at = :created_at,
	updated_at = :updated_at, completed_at = :completed_at, source = :source,
	expected_size = :expected_size, mime = :mime, category = :category, kind = :kind, media = :media,
	media_checkpoint = :media_checkpoint, completion_dialog = :completion_dialog, started_at = :started_at,
	active_ms = :active_ms, replace_file = :replace_file, served_type = :served_type,
	name_controls = :name_controls, page_url = :page_url, remote_name = :remote_name,
	rescued_bytes = :rescued_bytes
WHERE id = :id`

	setFileCheckSQL = `UPDATE downloads SET file_check = ? WHERE id = ?`

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
		sql.Named("served_type", d.ServedType),
		sql.Named("name_controls", d.NameControls),
		sql.Named("page_url", d.PageURL),
		sql.Named("remote_name", d.RemoteName),
		sql.Named("rescued_bytes", d.RescuedBytes),
	}, nil
}

// encodeFileCheck stores a file check as JSON; nil is NULL.
func encodeFileCheck(fc *model.FileCheck) (sql.NullString, error) {
	if fc == nil {
		return sql.NullString{}, nil
	}
	c := *fc
	c.CheckedAt = canonicalTime(c.CheckedAt)
	b, err := json.Marshal(&c)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("encode file check: %w", err)
	}
	return sql.NullString{String: string(b), Valid: true}, nil
}

// decodeFileCheck parses the stored file check. Invalid JSON reads as none: the check
// is advisory and never makes a record unreadable.
func decodeFileCheck(s sql.NullString) *model.FileCheck {
	if !s.Valid {
		return nil
	}
	var fc model.FileCheck
	if err := json.Unmarshal([]byte(s.String), &fc); err != nil {
		return nil
	}
	if !fc.CheckedAt.IsZero() {
		fc.CheckedAt = fc.CheckedAt.UTC()
	}
	return &fc
}

// rowScanner is implemented by *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanDownload decodes one row selected with downloadSelect.
func scanDownload(r rowScanner) (model.Download, error) {
	var raw rawDownload
	if err := raw.scan(r); err != nil {
		return model.Download{}, err
	}
	return raw.decode()
}

// rawDownload is a row selected with downloadSelect before its JSON columns are parsed:
// scanning stays on the row cursor, the parsing (most of a row's cost) can run anywhere.
type rawDownload struct {
	d                                 model.Download
	id, mode, state, maxConns, source int64
	kind, queueID                     int64
	headers, media, fileCheck         sql.NullString
	checkpoint                        []byte
	hasBody                           bool
	body                              []byte
	createdAt, updatedAt, completedAt sql.NullInt64
	startedAt                         sql.NullInt64
}

func (x *rawDownload) scan(r rowScanner) error {
	d := &x.d
	return r.Scan(&x.id, &d.Request.URL, &d.Request.Method, &x.headers, &x.hasBody, &x.body,
		&d.FinalURL, &d.Dir, &d.Filename, &d.Size, &x.mode, &x.state,
		&d.Validators.ETag, &d.Validators.StrongETag, &d.Validators.LastModified,
		&x.maxConns, &d.SpeedLimit, &d.Error, &x.createdAt, &x.updatedAt, &x.completedAt,
		&x.source, &d.ExpectedSize, &d.MIME, &d.Category, &x.kind, &x.media, &x.checkpoint,
		&d.CompletionDialog, &x.startedAt, &d.ActiveMS, &x.queueID, &d.ReplaceFile,
		&d.ServedType, &d.NameControls, &x.fileCheck, &d.PageURL, &d.RemoteName, &d.RescuedBytes)
}

func (x *rawDownload) decode() (model.Download, error) {
	d := x.d
	var err error
	d.FileCheck = decodeFileCheck(x.fileCheck)
	d.ID = model.DownloadID(x.id)
	if d.Request.Headers, err = decodeHeaders(x.headers); err != nil {
		return model.Download{}, fmt.Errorf("download %d: %w", x.id, err)
	}
	d.Request.Body = decodeBody(x.hasBody, x.body)
	d.Mode = model.Mode(x.mode)
	d.State = model.State(x.state)
	d.MaxConnections = int(x.maxConns)
	d.Source = model.Source(x.source)
	d.Kind = model.Kind(x.kind)
	d.QueueID = model.QueueID(x.queueID)
	if d.Media, err = decodeMedia(x.media, x.checkpoint); err != nil {
		return model.Download{}, fmt.Errorf("download %d: %w", x.id, err)
	}
	d.CreatedAt = decodeTime(x.createdAt)
	d.UpdatedAt = decodeTime(x.updatedAt)
	d.CompletedAt = decodeTime(x.completedAt)
	d.StartedAt = decodeTime(x.startedAt)
	return d, nil
}

// listDecoder parses the rows of a listing in chunks while the cursor goes on scanning:
// the JSON columns are most of a row's cost, so on up to GOMAXPROCS goroutines a listing
// of a long history takes about as long as reading its rows.
type listDecoder struct {
	chunks []*listPart
	sem    chan struct{}
	wg     sync.WaitGroup
}

type listPart struct {
	out []model.Download
	err error
}

// listChunk is how many rows one goroutine parses; a shorter listing is parsed on the
// cursor's goroutine.
const listChunk = 256

func newListDecoder() *listDecoder {
	return &listDecoder{sem: make(chan struct{}, runtime.GOMAXPROCS(0))}
}

// add parses rows: on the caller's goroutine when they are the whole listing, otherwise
// in the background.
func (l *listDecoder) add(rows []rawDownload, last bool) {
	part := &listPart{out: make([]model.Download, len(rows))}
	l.chunks = append(l.chunks, part)
	run := func() {
		for k := range rows {
			d, err := rows[k].decode()
			if err != nil {
				part.err = err
				return
			}
			part.out[k] = d
		}
	}
	if last && len(l.chunks) == 1 {
		run()
		return
	}
	l.sem <- struct{}{}
	l.wg.Add(1)
	go func() {
		defer func() { <-l.sem; l.wg.Done() }()
		run()
	}()
}

// wait returns the rows in order, or the first failing row's error in row order (as a
// sequential parse would). Every add must have been made before it.
func (l *listDecoder) wait() ([]model.Download, error) {
	l.wg.Wait()
	n := 0
	for _, p := range l.chunks {
		if p.err != nil {
			return nil, p.err
		}
		n += len(p.out)
	}
	if len(l.chunks) == 1 {
		return l.chunks[0].out, nil
	}
	out := make([]model.Download, 0, n)
	for _, p := range l.chunks {
		out = append(out, p.out...)
	}
	return out, nil
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
