package secrets

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// fileKeyring stores each secret as a 0600 file in a directory. It is the Linux backend
// and the Windows DPAPI ciphertext fallback. On Linux the bytes are not OS-encrypted,
// so Open logs a warning once.
type fileKeyring struct {
	dir string
	mu  sync.Mutex
}

// NewFileKeyring returns a keyring that stores secrets as 0600 files under dir. The
// directory is created with mode 0700. The stored bytes are not encrypted; callers that
// want OS protection use Open, which selects DPAPI or the keychain where available.
func NewFileKeyring(dir string) (Keyring, error) {
	if dir == "" {
		return nil, errors.New("secrets: file keyring needs a directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("secrets: create directory: %w", err)
	}
	return &fileKeyring{dir: dir}, nil
}

// fileName maps a secret name to a file name that is safe on every OS (no ':' or path
// separators) and cannot collide: a sanitised, lower-cased prefix plus a hash of the
// exact name. The hash preserves identity even when the sanitised prefix collides.
func (k *fileKeyring) fileName(name string) string {
	sum := sha256.Sum256([]byte(name))
	return sanitizeBaseName(name) + "." + hex.EncodeToString(sum[:8]) + ".secret"
}

// sanitizeBaseName replaces characters that are unsafe in a file name with '_'.
func sanitizeBaseName(name string) string {
	b := []byte(name)
	for i, c := range b {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			b[i] = '_'
		}
	}
	return string(b)
}

func (k *fileKeyring) path(name string) string {
	return filepath.Join(k.dir, k.fileName(name))
}

func (k *fileKeyring) Set(name string, secret []byte) error {
	if err := checkSet(name, secret); err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return writeFileAtomic(k.path(name), secret, k.dir)
}

func (k *fileKeyring) Get(name string) ([]byte, error) {
	if !validName(name) {
		return nil, fmt.Errorf("secrets: invalid name %q", name)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	b, err := os.ReadFile(k.path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("secrets: read: %w", err)
	}
	return b, nil
}

func (k *fileKeyring) Delete(name string) error {
	if !validName(name) {
		return fmt.Errorf("secrets: invalid name %q", name)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := os.Remove(k.path(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("secrets: delete: %w", err)
	}
	return nil
}

// writeFileAtomic writes data to a new 0600 temp file in dir and renames it over path.
func writeFileAtomic(path string, data []byte, dir string) (err error) {
	f, err := os.CreateTemp(dir, ".secret-*.tmp") // 0600
	if err != nil {
		return fmt.Errorf("secrets: write: %w", err)
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("secrets: write: %w", werr)
	}
	if err = os.Rename(tmp, path); err != nil {
		return fmt.Errorf("secrets: replace: %w", err)
	}
	return nil
}

// fileBlobStore is a BlobStore backed by 0600 files (the default Windows blob
// persistence when the daemon provides none).
type fileBlobStore struct{ k *fileKeyring }

// NewFileBlobStore returns a file-backed BlobStore under dir.
func NewFileBlobStore(dir string) (BlobStore, error) {
	k, err := NewFileKeyring(dir)
	if err != nil {
		return nil, err
	}
	return &fileBlobStore{k: k.(*fileKeyring)}, nil
}

func (s *fileBlobStore) PutBlob(name string, blob []byte) error {
	s.k.mu.Lock()
	defer s.k.mu.Unlock()
	return writeFileAtomic(s.k.path(name), blob, s.k.dir)
}

func (s *fileBlobStore) GetBlob(name string) ([]byte, bool, error) {
	s.k.mu.Lock()
	defer s.k.mu.Unlock()
	b, err := os.ReadFile(s.k.path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

func (s *fileBlobStore) DeleteBlob(name string) error {
	s.k.mu.Lock()
	defer s.k.mu.Unlock()
	if err := os.Remove(s.k.path(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
