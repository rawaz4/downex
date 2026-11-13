package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// This file owns the secrets table of migrations/0006_secrets.sql: opaque, already
// encrypted blobs by name (docs/M6-SPEC.md §2; the Windows DPAPI keyring keeps its
// ciphertext here). The methods match secrets.BlobStore, which cmd/downexd hands to the
// keyring; the store itself never decrypts anything.

// secretTimeout bounds one secret read or write: the keyring's interface has no context.
const secretTimeout = 10 * time.Second

// maxSecretBlob bounds a stored blob (the keyring's own limit is smaller).
const maxSecretBlob = 1 << 20

// PutBlob stores blob under name, replacing an earlier one.
func (s *SQLite) PutBlob(name string, blob []byte) error {
	if name == "" || len(blob) == 0 || len(blob) > maxSecretBlob {
		return fmt.Errorf("store: put secret: %w: empty name or blob, or blob larger than %d bytes", ErrInvalid, maxSecretBlob)
	}
	ctx, cancel := context.WithTimeout(context.Background(), secretTimeout)
	defer cancel()
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO secrets (name, blob) VALUES (?, ?) ON CONFLICT (name) DO UPDATE SET blob = excluded.blob`,
			name, blob)
		return err
	})
	if err != nil {
		return fmt.Errorf("store: put secret: %w", err)
	}
	return nil
}

// GetBlob returns the blob stored under name; ok is false when there is none.
func (s *SQLite) GetBlob(name string) (blob []byte, ok bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), secretTimeout)
	defer cancel()
	if err := s.checkOpen(ctx); err != nil {
		return nil, false, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT blob FROM secrets WHERE name = ?`, name).Scan(&blob)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("store: get secret: %w", err)
	}
	return blob, true, nil
}

// DeleteBlob removes the blob stored under name; a missing one is not an error.
func (s *SQLite) DeleteBlob(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), secretTimeout)
	defer cancel()
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE name = ?`, name)
		return err
	})
	if err != nil {
		return fmt.Errorf("store: delete secret: %w", err)
	}
	return nil
}
