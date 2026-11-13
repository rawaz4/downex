package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
)

// migrationFiles holds the forward-only schema migrations. File names are
// "NNNN_description.sql"; versions must start at 1 and be contiguous.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationDir is the directory inside migrationFiles that holds the scripts.
const migrationDir = "migrations"

// migration is one schema step that moves the database to version.
type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations reads and orders the migration scripts in dir of fsys.
func loadMigrations(fsys fs.FS, dir string) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, dir) // sorted by file name
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var out []migration
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".sql" {
			continue
		}
		prefix, _, ok := strings.Cut(e.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("migration %q: name must be NNNN_description.sql", e.Name())
		}
		v, err := strconv.Atoi(prefix)
		if err != nil || v < 1 {
			return nil, fmt.Errorf("migration %q: invalid version prefix %q", e.Name(), prefix)
		}
		if want := len(out) + 1; v != want {
			return nil, fmt.Errorf("migration %q: version %d, want %d (versions must be contiguous from 1)", e.Name(), v, want)
		}
		body, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", e.Name(), err)
		}
		out = append(out, migration{version: v, name: e.Name(), sql: string(body)})
	}
	if len(out) == 0 {
		return nil, errors.New("no migrations found")
	}
	return out, nil
}

// schemaVersionDDL creates the single-row table that records the applied version.
const schemaVersionDDL = `CREATE TABLE IF NOT EXISTS schema_version (
	id      INTEGER PRIMARY KEY CHECK (id = 1),
	version INTEGER NOT NULL
) STRICT`

// migrate brings db to the latest version in ms inside one write transaction,
// so a concurrent opener (in this or another process) waits and then sees the
// finished schema. A database newer than ms is rejected without changes.
func migrate(ctx context.Context, db *sql.DB, ms []migration) (err error) {
	tx, err := db.BeginTx(ctx, nil) // BEGIN IMMEDIATE via the DSN's _txlock
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer func() {
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
				err = errors.Join(err, fmt.Errorf("rollback migration: %w", rbErr))
			}
		}
	}()

	if _, err = tx.ExecContext(ctx, schemaVersionDDL); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}
	current, err := readSchemaVersion(ctx, tx)
	if err != nil {
		return err
	}
	latest := len(ms)
	if current > latest {
		return fmt.Errorf("%w: database is at version %d, this build supports up to %d", ErrUnsupportedSchema, current, latest)
	}
	for _, m := range ms[current:] {
		if _, err = tx.ExecContext(ctx, m.sql); err != nil {
			return fmt.Errorf("apply migration %s: %w", m.name, err)
		}
	}
	if current < latest {
		const upsert = `INSERT INTO schema_version (id, version) VALUES (1, ?)
			ON CONFLICT (id) DO UPDATE SET version = excluded.version`
		if _, err = tx.ExecContext(ctx, upsert, latest); err != nil {
			return fmt.Errorf("record schema version %d: %w", latest, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}

// readSchemaVersion returns the applied version, 0 for a new database.
func readSchemaVersion(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}) (int, error) {
	var v int
	err := q.QueryRowContext(ctx, `SELECT version FROM schema_version WHERE id = 1`).Scan(&v)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("read schema version: %w", err)
	case v < 0:
		return 0, fmt.Errorf("%w: negative version %d", ErrUnsupportedSchema, v)
	}
	return v, nil
}
