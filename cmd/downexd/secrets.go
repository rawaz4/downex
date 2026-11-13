package main

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"

	"downex/internal/secrets"
	"downex/internal/store"
)

// secretsDirName is the folder next to the database that holds the passwords of daemons
// without an OS keyring of their own (Linux; private macOS instances).
const secretsDirName = "secrets"

// openKeyring returns where the daemon keeps the proxy and site-login passwords
// (docs/M6-SPEC.md §2; DaemonService.SetSecret):
//
//   - Windows: DPAPI ciphertext (current user) in the database's secrets table, for every
//     daemon: the blobs live with that daemon's database.
//   - macOS: the user's own daemon uses the login keychain (generic passwords of the
//     service "Downex"); a private instance (--db, --endpoint, $DOWNEX_ENDPOINT: tests,
//     scripts) keeps 0600 files next to its database instead, so that it never touches
//     the user's keychain.
//   - Linux and other systems: 0600 files next to the database (not OS-encrypted; a
//     warning is logged).
func openKeyring(own bool, dbPath string, st *store.SQLite, log *slog.Logger) (secrets.Keyring, error) {
	dir := filepath.Join(filepath.Dir(dbPath), secretsDirName)
	warn := func(format string, args ...any) { log.Warn(fmt.Sprintf(format, args...)) }
	switch {
	case runtime.GOOS == "windows":
		return secrets.Open(secrets.Config{Blobs: st})
	case runtime.GOOS == "darwin" && own:
		return secrets.Open(secrets.Config{Service: secrets.DefaultService})
	case runtime.GOOS == "darwin":
		log.Info("this private daemon keeps passwords in files next to its database, not in the keychain", "dir", dir)
		return secrets.NewFileKeyring(dir)
	}
	return secrets.Open(secrets.Config{Dir: dir, Warnf: warn})
}
