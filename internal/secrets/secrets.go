// Package secrets stores passwords for proxy and site-login authentication in the
// operating system's protected storage (docs/M6-SPEC.md §2). Passwords never appear in
// settings, logs, Watch events, error texts or exports; only this package reads them
// back, to answer an authentication challenge.
//
// Backends: Windows encrypts each secret with DPAPI (current-user scope) and keeps the
// blob through a BlobStore the daemon provides; macOS stores a generic password in the
// login keychain via /usr/bin/security; Linux writes a 0600 file in the config dir and
// logs that the secret is not OS-encrypted. The names are opaque identifiers assigned
// by the daemon (e.g. "proxy", "site_login:<id>").
package secrets

import (
	"errors"
	"fmt"
	"regexp"
)

// ErrNotFound is returned by Get for a name with no stored secret.
var ErrNotFound = errors.New("secrets: not found")

// DefaultService is the macOS keychain service name.
const DefaultService = "Downex"

// maxSecretBytes bounds a stored secret.
const maxSecretBytes = 64 << 10

// namePattern restricts secret names to a safe identifier form so they cannot inject
// into keychain arguments or file paths.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._:-]{0,126})$`)

// validName reports whether name is an acceptable secret identifier.
func validName(name string) bool { return namePattern.MatchString(name) }

// Keyring stores and retrieves secrets by name. Implementations are safe for
// concurrent use.
type Keyring interface {
	// Set stores secret under name, replacing any existing value.
	Set(name string, secret []byte) error
	// Get returns the secret stored under name, or ErrNotFound.
	Get(name string) ([]byte, error)
	// Delete removes the secret under name; removing a missing name is not an error.
	Delete(name string) error
}

// BlobStore persists opaque encrypted blobs by name (the Windows DPAPI backend keeps
// its ciphertext here; the daemon backs it with the database or a file).
type BlobStore interface {
	PutBlob(name string, blob []byte) error
	GetBlob(name string) ([]byte, bool, error)
	DeleteBlob(name string) error
}

// Config configures Open.
type Config struct {
	// Service is the macOS keychain service (default DefaultService).
	Service string
	// Dir is the directory for the Linux file backend (required there) and the default
	// file BlobStore on Windows when Blobs is nil.
	Dir string
	// Blobs persists the Windows DPAPI ciphertext. nil selects a 0600 file BlobStore
	// under Dir.
	Blobs BlobStore
	// Warnf, if set, receives a one-time warning from the Linux backend that secrets are
	// not OS-encrypted.
	Warnf func(format string, args ...any)
}

func (c Config) service() string {
	if c.Service == "" {
		return DefaultService
	}
	return c.Service
}

// checkSet validates a Set call's arguments for every backend.
func checkSet(name string, secret []byte) error {
	if !validName(name) {
		return fmt.Errorf("secrets: invalid name %q", name)
	}
	if len(secret) == 0 {
		return errors.New("secrets: empty secret")
	}
	if len(secret) > maxSecretBytes {
		return fmt.Errorf("secrets: secret exceeds %d bytes", maxSecretBytes)
	}
	return nil
}
