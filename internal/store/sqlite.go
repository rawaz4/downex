package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" database/sql driver
)

// Connection settings. Every pooled connection gets the same pragmas from the DSN.
const (
	sqliteDriver = "sqlite"
	// busyTimeoutMS bounds how long a connection waits for a lock held by
	// another connection or process before failing with SQLITE_BUSY.
	busyTimeoutMS = 5000
	// maxSQLiteConns bounds the pool. WAL lets readers proceed while the single
	// writer commits; each connection costs a page cache, so keep it small.
	maxSQLiteConns = 4
)

// SQLite is the durable Store backed by a SQLite database file.
//
// Every connection runs with journal_mode=WAL, synchronous=FULL (a committed
// checkpoint survives power loss), foreign_keys=ON and busy_timeout=5000.
// Write transactions start with BEGIN IMMEDIATE and are serialised in-process
// by a context-aware semaphore, so concurrent writers queue instead of
// failing with SQLITE_BUSY; readers use the pool concurrently.
type SQLite struct {
	db       *sql.DB
	writeSem chan struct{} // capacity 1: held for the duration of a write transaction
	done     chan struct{} // closed by Close

	closeOnce sync.Once
	closeErr  error
}

var _ Store = (*SQLite)(nil)

// OpenSQLite opens (creating if needed) the database at path and migrates it
// to the latest schema. The parent directory must exist. Opening the same file
// again, from this or another process, is safe: migrations are idempotent and
// run under a write lock.
func OpenSQLite(path string) (*SQLite, error) {
	dsn, err := sqliteDSN(path)
	if err != nil {
		return nil, fmt.Errorf("store: open sqlite %q: %w", path, err)
	}
	ms, err := loadMigrations(migrationFiles, migrationDir)
	if err != nil {
		return nil, fmt.Errorf("store: open sqlite %q: %w", path, err)
	}
	db, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open sqlite %q: %w", path, err)
	}
	db.SetMaxOpenConns(maxSQLiteConns)
	db.SetMaxIdleConns(maxSQLiteConns)

	ctx := context.Background()
	if err := verifyPragmas(ctx, db); err != nil {
		return nil, errors.Join(fmt.Errorf("store: open sqlite %q: %w", path, err), db.Close())
	}
	if err := migrate(ctx, db, ms); err != nil {
		return nil, errors.Join(fmt.Errorf("store: open sqlite %q: %w", path, err), db.Close())
	}
	return &SQLite{
		db:       db,
		writeSem: make(chan struct{}, 1),
		done:     make(chan struct{}),
	}, nil
}

// sqliteDSN builds the driver DSN for a database file. The path is made
// absolute so it can never be mistaken for a "file:" URI, and paths containing
// '?' are rejected because the driver splits the DSN at the first '?'.
func sqliteDSN(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%w: empty database path", ErrInvalid)
	}
	if strings.ContainsRune(path, '?') {
		return "", fmt.Errorf("%w: database path must not contain '?'", ErrInvalid)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve database path: %w", err)
	}
	q := url.Values{}
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMS))
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(FULL)")
	q.Add("_pragma", "foreign_keys(ON)")
	q.Set("_txlock", "immediate")
	return abs + "?" + q.Encode(), nil
}

// verifyPragmas checks that the connection settings the store's guarantees
// rest on (durability, cascading deletes, WAL concurrency) are in effect, so
// a driver that silently ignored the DSN fails loudly instead.
func verifyPragmas(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()

	var journal string
	if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
		return fmt.Errorf("read journal_mode: %w", err)
	}
	if !strings.EqualFold(journal, "wal") {
		return fmt.Errorf("journal_mode is %q, want wal (the filesystem may not support WAL)", journal)
	}
	for _, p := range []struct {
		name string
		want int64
	}{
		{"synchronous", 2}, // FULL
		{"foreign_keys", 1},
		{"busy_timeout", busyTimeoutMS},
	} {
		var got int64
		if err := conn.QueryRowContext(ctx, "PRAGMA "+p.name).Scan(&got); err != nil {
			return fmt.Errorf("read %s: %w", p.name, err)
		}
		if got != p.want {
			return fmt.Errorf("pragma %s is %d, want %d", p.name, got, p.want)
		}
	}
	return nil
}

// Close closes the database. In-flight operations finish first; later calls
// fail with ErrClosed. Closing twice returns the first result.
func (s *SQLite) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		s.closeErr = s.db.Close()
	})
	return s.closeErr
}

// checkOpen fails fast for a cancelled context or a closed store.
func (s *SQLite) checkOpen(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-s.done:
		return ErrClosed
	default:
		return nil
	}
}

// writeTx runs fn in an IMMEDIATE transaction while holding the write
// semaphore. fn's error rolls the transaction back and is returned as is.
func (s *SQLite) writeTx(ctx context.Context, fn func(tx *sql.Tx) error) (err error) {
	if err := s.checkOpen(ctx); err != nil {
		return err
	}
	select {
	case s.writeSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return ErrClosed
	}
	defer func() { <-s.writeSem }()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return errors.Join(err, fmt.Errorf("rollback: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
