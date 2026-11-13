// Command release-sign manages the ed25519 key that Downex update manifests are signed
// with, and signs and verifies manifests (docs/M6-SPEC.md §4, internal/update).
//
// Usage:
//
//	go run ./scripts/release-sign keygen -out <private key file>
//	go run ./scripts/release-sign sign [-key <private key file>] [-out <manifest>.sig] <manifest.json>
//	go run ./scripts/release-sign verify -pub <public key> [-sig <manifest>.sig] <manifest.json>
//	go run ./scripts/release-sign asset -os <goos> -arch <goarch|universal> -url <https URL> <package file>
//
// keygen writes a new private key (the base64 of its 32-byte seed, mode 0600) to a new
// file, which must lie outside the Downex repository, and prints the public key. Release
// builds compile that public key in:
//
//	-ldflags "-X downex/internal/update.PublicKey=<public key>"
//
// sign reads the private key from -key, or from $DOWNEX_UPDATE_PRIVATE_KEY (the same
// base64) without -key, checks the manifest as the daemon will (internal/update
// ParseManifest), signs its exact bytes and writes the base64 signature to
// <manifest>.sig (upload both next to each other: the feed URL names the manifest, the
// daemon fetches <feed URL>.sig). It prints the public key that verifies the signature.
// verify checks a manifest and its signature with a public key. asset prints the asset
// entry of a package (size and SHA-256) for the manifest's "assets" list.
//
// The private key never enters the repository.
package main

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"downex/internal/update"
)

// envPrivateKey names the environment variable that holds the private key for sign.
const envPrivateKey = "DOWNEX_UPDATE_PRIVATE_KEY"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

const usage = `Usage:
  release-sign keygen -out <private key file>
  release-sign sign [-key <private key file>] [-out <manifest>.sig] <manifest.json>
  release-sign verify -pub <public key> [-sig <manifest>.sig] <manifest.json>
  release-sign asset -os <goos> -arch <goarch|universal> -url <https URL> <package file>
`

func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "keygen":
		err = keygen(args[1:], stdout, stderr)
	case "sign":
		err = signCmd(args[1:], stdout, stderr, getenv)
	case "verify":
		err = verifyCmd(args[1:], stdout, stderr)
	case "asset":
		err = assetCmd(args[1:], stdout, stderr)
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "release-sign: unknown command %q\n%s", args[0], usage)
		return 2
	}
	var ue usageError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ue):
		fmt.Fprintf(stderr, "release-sign %s: %v\n", args[0], err)
		return 2
	}
	fmt.Fprintf(stderr, "release-sign %s: %v\n", args[0], err)
	return 1
}

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func newFlags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("release-sign "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func parse(fs *flag.FlagSet, args []string, positional int) error {
	if err := fs.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	if fs.NArg() != positional {
		return usageError{fmt.Sprintf("want %d file argument(s), got %d", positional, fs.NArg())}
	}
	return nil
}

func keygen(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("keygen", stderr)
	out := fs.String("out", "", "new `file` for the private key (outside the repository)")
	if err := parse(fs, args, 0); err != nil {
		return err
	}
	if *out == "" {
		return usageError{"-out is required"}
	}
	path, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	if repo, ok := insideRepository(path); ok {
		return fmt.Errorf("refusing to write a private key into the Downex repository (%s); choose a file outside it", repo)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err // never overwrites a key
	}
	_, werr := f.WriteString(base64.StdEncoding.EncodeToString(priv.Seed()) + "\n")
	if err := f.Close(); werr == nil {
		werr = err
	}
	if werr != nil {
		_ = os.Remove(path)
		return werr
	}
	fmt.Fprintln(stdout, base64.StdEncoding.EncodeToString(pub))
	fmt.Fprintf(stderr, "Private key written to %s (keep it secret and out of the repository).\n"+
		"The line above is the public key: build with -ldflags \"-X downex/internal/update.PublicKey=<public key>\".\n", path)
	return nil
}

// insideRepository reports whether path lies in a directory tree whose go.mod declares
// module downex, and returns that directory.
func insideRepository(path string) (string, bool) {
	for dir := filepath.Dir(path); ; {
		if data, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil && declaresDownex(data) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func declaresDownex(gomod []byte) bool {
	sc := bufio.NewScanner(bytes.NewReader(gomod))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 && f[0] == "module" && strings.Trim(f[1], `"`) == "downex" {
			return true
		}
	}
	return false
}

// parsePrivateKey decodes a private key: the base64 of a 32-byte seed (keygen) or of a
// 64-byte ed25519 private key.
func parsePrivateKey(text string) (ed25519.PrivateKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(text))
	switch {
	case err != nil:
		return nil, errors.New("the private key is not base64")
	case len(b) == ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(b), nil
	case len(b) == ed25519.PrivateKeySize:
		k := ed25519.PrivateKey(b)
		if !bytes.Equal(ed25519.NewKeyFromSeed(k.Seed()), k) {
			return nil, errors.New("the private key is inconsistent")
		}
		return k, nil
	}
	return nil, fmt.Errorf("the private key has %d bytes, want %d (seed) or %d", len(b), ed25519.SeedSize, ed25519.PrivateKeySize)
}

func signCmd(args []string, stdout, stderr io.Writer, getenv func(string) string) error {
	fs := newFlags("sign", stderr)
	keyFile := fs.String("key", "", "private key `file` (default: $"+envPrivateKey+")")
	out := fs.String("out", "", "signature `file` (default: <manifest>.sig)")
	if err := parse(fs, args, 1); err != nil {
		return err
	}
	manifestPath := fs.Arg(0)
	var keyText string
	if *keyFile != "" {
		b, err := os.ReadFile(*keyFile)
		if err != nil {
			return err
		}
		keyText = string(b)
	} else if keyText = getenv(envPrivateKey); keyText == "" {
		return usageError{"no private key: pass -key or set $" + envPrivateKey}
	}
	priv, err := parsePrivateKey(keyText)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	if len(data) > update.MaxManifestSize {
		return fmt.Errorf("the manifest is larger than %d bytes", update.MaxManifestSize)
	}
	m, err := update.ParseManifest(data)
	if err != nil {
		return err
	}
	sigFile := update.EncodeSignature(ed25519.Sign(priv, data))
	pub := priv.Public().(ed25519.PublicKey)
	if err := update.Verify(pub, data, sigFile); err != nil {
		return fmt.Errorf("self-check: %w", err)
	}
	if *out == "" {
		*out = manifestPath + ".sig"
	}
	if err := os.WriteFile(*out, sigFile, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "signed Downex %s (%d assets): %s\npublic key: %s\n", m.Version, len(m.Assets), *out,
		base64.StdEncoding.EncodeToString(pub))
	return nil
}

func verifyCmd(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("verify", stderr)
	pubText := fs.String("pub", "", "base64 public `key` (as compiled into the build)")
	sigPath := fs.String("sig", "", "signature `file` (default: <manifest>.sig)")
	if err := parse(fs, args, 1); err != nil {
		return err
	}
	if *pubText == "" {
		return usageError{"-pub is required"}
	}
	pub, err := update.ParsePublicKey(*pubText)
	if err != nil {
		return err
	}
	manifestPath := fs.Arg(0)
	if *sigPath == "" {
		*sigPath = manifestPath + ".sig"
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(*sigPath)
	if err != nil {
		return err
	}
	if len(data) > update.MaxManifestSize || len(sig) > update.MaxSignatureSize {
		return errors.New("the manifest or its signature is larger than the daemon accepts")
	}
	if err := update.Verify(pub, data, sig); err != nil {
		return err
	}
	m, err := update.ParseManifest(data)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "OK: Downex %s, %d assets\n", m.Version, len(m.Assets))
	return nil
}

func assetCmd(args []string, stdout, stderr io.Writer) error {
	fs := newFlags("asset", stderr)
	goos := fs.String("os", "", "`goos` of the package: windows, darwin or linux")
	arch := fs.String("arch", "", "`goarch` of the package (amd64, arm64, 386), or universal (darwin)")
	url := fs.String("url", "", "https `URL` the package is published at")
	if err := parse(fs, args, 1); err != nil {
		return err
	}
	f, err := os.Open(fs.Arg(0))
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	a := update.Asset{OS: *goos, Arch: *arch, URL: *url, SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}
	// The daemon's rules decide what a valid asset is.
	probe, err := json.Marshal(update.Manifest{Version: "0.0.0", Assets: []update.Asset{a}})
	if err != nil {
		return err
	}
	if _, err := update.ParseManifest(probe); err != nil {
		return err
	}
	out, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s\n", out)
	return nil
}
