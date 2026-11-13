// Package update checks for and installs new Downex releases (docs/M6-SPEC.md §4).
//
// A release is described by a JSON manifest (Manifest) at the user's update feed URL and
// a detached ed25519 signature at <feed URL>.sig: the base64 of the signature over the
// manifest's exact bytes. The signature is verified with the public key compiled into the
// build (PublicKey) before anything in the manifest is parsed; without a key updates are
// disabled (ErrDisabled). Every URL is https, also after redirects. A package is
// downloaded with at most its published size, checked against that size and its SHA-256
// and only then handed to the Installer of the running system: Windows runs the per-user
// Setup silently, macOS replaces Downex.app from the disk image after checking that its
// code signature belongs to the same team as the running one (an ad-hoc signed running
// build: that the new bundle is validly signed and is the manifest's version).
//
// The package has no global state besides PublicKey and DefaultFeedURL. Commands, process starts, the
// running executable and the environment are injected (System), so that every
// platform's rules are tested on every OS.
package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// PublicKey is the base64 (standard encoding) ed25519 public key that release manifests
// are signed with (scripts/release-sign). Release builds set it with
//
//	-ldflags "-X downex/internal/update.PublicKey=<base64>"
//
// Empty disables updates. The private key never enters the repository.
var PublicKey string

// DefaultFeedURL is the https URL of the release feed's signed manifest that a new
// installation checks (the daemon's default update feed). Release builds set it with
//
//	-ldflags "-X downex/internal/update.DefaultFeedURL=<https URL>"
//
// (scripts/build-windows.ps1 and build-macos.sh take it from $DOWNEX_UPDATE_FEED_URL).
// Empty leaves a new installation without a feed, so updates stay off until the user
// sets one. Read it through DefaultFeed, which refuses an invalid value.
var DefaultFeedURL string

// DefaultFeed returns DefaultFeedURL when it is a valid feed URL (CheckFeedURL) and ""
// otherwise, with an error that says why: a build with a malformed default must not hand
// it to every installation's settings.
func DefaultFeed() (string, error) {
	if DefaultFeedURL == "" {
		return "", nil
	}
	if _, err := CheckFeedURL(DefaultFeedURL); err != nil {
		return "", fmt.Errorf("the default update feed of this build is invalid: %w", err)
	}
	return DefaultFeedURL, nil
}

// Size limits.
const (
	// MaxManifestSize bounds the manifest (1 MiB).
	MaxManifestSize = 1 << 20
	// MaxSignatureSize bounds the signature file: 88 base64 bytes and some white space.
	MaxSignatureSize = 4 << 10
	// MaxAssetSize bounds a package (4 GiB).
	MaxAssetSize = 4 << 30
)

// Errors. Every error of the package wraps one of them (errors.Is), so that callers map
// them to their own codes; messages never contain URLs.
var (
	// ErrDisabled: the build has no update signing key (PublicKey is empty).
	ErrDisabled = errors.New("updates are disabled: this build has no update signing key")
	// ErrBadKey: PublicKey is not a base64 ed25519 public key.
	ErrBadKey = errors.New("updates are disabled: the update signing key of this build is invalid")
	// ErrInsecureURL: a feed, package or redirect URL is not https.
	ErrInsecureURL = errors.New("update URLs must use https")
	// ErrFetch: the manifest, its signature or the package could not be fetched (network
	// failure or an HTTP status other than 200).
	ErrFetch = errors.New("cannot fetch the update")
	// ErrNotPublished: the feed has no manifest yet (HTTP 404 or 410 for the manifest). It
	// wraps ErrFetch too.
	ErrNotPublished = errors.New("no update information is published yet")
	// ErrTooLarge: a response is larger than its limit.
	ErrTooLarge = errors.New("too large")
	// ErrSignature: the signature is malformed or does not verify the manifest.
	ErrSignature = errors.New("the update manifest's signature is not valid")
	// ErrManifest: a correctly signed manifest is malformed or invalid.
	ErrManifest = errors.New("invalid update manifest")
	// ErrVersion: a version is not a semantic version.
	ErrVersion = errors.New("invalid version")
	// ErrSize: the package does not have its published size.
	ErrSize = errors.New("the downloaded update does not have its published size")
	// ErrChecksum: the package does not have its published SHA-256.
	ErrChecksum = errors.New("the downloaded update does not match its SHA-256 checksum")
	// ErrUnsupported: this system cannot install updates (Linux: the package manager does).
	ErrUnsupported = errors.New("installing updates is not supported on this system")
	// ErrNotUpdatable: the running installation cannot be replaced in place (not installed
	// with Setup, not inside Downex.app, a translocated or read-only copy).
	ErrNotUpdatable = errors.New("this installation cannot be updated in place")
	// ErrCodeSignature: the package's code signature is invalid or belongs to someone else
	// than the running installation's, or (an ad-hoc signed installation) the package is not
	// the manifest's version.
	ErrCodeSignature = errors.New("the update's code signature does not match this installation")
)

// ParsePublicKey decodes a base64 (standard encoding) ed25519 public key. An empty
// string yields ErrDisabled, anything else that is not 32 bytes of base64 ErrBadKey.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, ErrDisabled
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, ErrBadKey
	}
	return ed25519.PublicKey(b), nil
}

// DecodeSignature decodes the contents of a signature file: the base64 (standard
// encoding) of a 64-byte ed25519 signature, with optional surrounding white space.
func DecodeSignature(data []byte) ([]byte, error) {
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, fmt.Errorf("%w: not the base64 of an ed25519 signature", ErrSignature)
	}
	return sig, nil
}

// EncodeSignature is the signature file's contents for sig (DecodeSignature's inverse).
func EncodeSignature(sig []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(sig) + "\n")
}

// Verify checks that sigFile (a signature file's contents) is a valid signature of the
// exact bytes manifest under key.
func Verify(key ed25519.PublicKey, manifest, sigFile []byte) error {
	if len(key) != ed25519.PublicKeySize {
		return ErrBadKey
	}
	sig, err := DecodeSignature(sigFile)
	if err != nil {
		return err
	}
	if !ed25519.Verify(key, manifest, sig) {
		return ErrSignature
	}
	return nil
}
