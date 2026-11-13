//go:build darwin

package secrets

// Open returns the macOS keychain-backed keyring (keychain.go).
func Open(cfg Config) (Keyring, error) {
	return &keychainKeyring{service: cfg.service()}, nil
}
