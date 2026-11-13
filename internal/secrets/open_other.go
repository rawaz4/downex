//go:build !windows && !darwin

package secrets

import "errors"

// Open returns a file-backed keyring (the Linux behaviour). The secret bytes are not
// OS-encrypted, so a warning is emitted once if Config.Warnf is set.
func Open(cfg Config) (Keyring, error) {
	if cfg.Dir == "" {
		return nil, errors.New("secrets: Config.Dir is required")
	}
	k, err := NewFileKeyring(cfg.Dir)
	if err != nil {
		return nil, err
	}
	if cfg.Warnf != nil {
		cfg.Warnf("storing secrets unencrypted in %s (no OS keyring configured)", cfg.Dir)
	}
	return k, nil
}
