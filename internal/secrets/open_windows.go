//go:build windows

package secrets

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// cryptprotectUIForbidden suppresses any DPAPI prompt (headless daemon).
const cryptprotectUIForbidden = 0x1

// dpapiKeyring encrypts each secret with DPAPI under the current-user scope and keeps
// the ciphertext in a BlobStore.
type dpapiKeyring struct {
	blobs BlobStore
}

// Open returns the Windows DPAPI-backed keyring. Config.Blobs persists the ciphertext;
// when nil a 0600 file BlobStore under Config.Dir is used.
func Open(cfg Config) (Keyring, error) {
	blobs := cfg.Blobs
	if blobs == nil {
		if cfg.Dir == "" {
			return nil, errors.New("secrets: Config.Dir or Config.Blobs is required")
		}
		var err error
		if blobs, err = NewFileBlobStore(cfg.Dir); err != nil {
			return nil, err
		}
	}
	return &dpapiKeyring{blobs: blobs}, nil
}

func (k *dpapiKeyring) Set(name string, secret []byte) error {
	if err := checkSet(name, secret); err != nil {
		return err
	}
	blob, err := dpapiProtect(secret)
	if err != nil {
		return fmt.Errorf("secrets: CryptProtectData: %w", err)
	}
	return k.blobs.PutBlob(name, blob)
}

func (k *dpapiKeyring) Get(name string) ([]byte, error) {
	if !validName(name) {
		return nil, fmt.Errorf("secrets: invalid name %q", name)
	}
	blob, ok, err := k.blobs.GetBlob(name)
	if err != nil {
		return nil, fmt.Errorf("secrets: read blob: %w", err)
	}
	if !ok {
		return nil, ErrNotFound
	}
	plain, err := dpapiUnprotect(blob)
	if err != nil {
		return nil, fmt.Errorf("secrets: CryptUnprotectData: %w", err)
	}
	return plain, nil
}

func (k *dpapiKeyring) Delete(name string) error {
	if !validName(name) {
		return fmt.Errorf("secrets: invalid name %q", name)
	}
	return k.blobs.DeleteBlob(name)
}

// dpapiProtect encrypts plain with DPAPI (current-user scope).
func dpapiProtect(plain []byte) ([]byte, error) {
	in := newBlob(plain)
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, cryptprotectUIForbidden, &out); err != nil {
		return nil, err
	}
	defer localFree(out.Data)
	return copyBlob(out), nil
}

// dpapiUnprotect decrypts a DPAPI blob.
func dpapiUnprotect(blob []byte) ([]byte, error) {
	in := newBlob(blob)
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, cryptprotectUIForbidden, &out); err != nil {
		return nil, err
	}
	defer localFree(out.Data)
	return copyBlob(out), nil
}

func newBlob(b []byte) windows.DataBlob {
	d := windows.DataBlob{Size: uint32(len(b))}
	if len(b) > 0 {
		d.Data = &b[0]
	}
	return d
}

func copyBlob(d windows.DataBlob) []byte {
	if d.Size == 0 || d.Data == nil {
		return []byte{}
	}
	return append([]byte(nil), unsafe.Slice(d.Data, d.Size)...)
}

func localFree(p *byte) {
	if p != nil {
		_, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(p)))
	}
}
