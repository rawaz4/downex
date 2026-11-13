//go:build !windows

package update

// Signer is who signed a file with Authenticode (VerifyAuthenticode).
type Signer struct {
	// Subject is the simple display name (the common name) of the signing certificate's
	// subject, e.g. "Microsoft Corporation"; "" when it could not be read.
	Subject string
}

// authenticode: only Windows has Authenticode; files elsewhere count as unsigned.
func authenticode(string) error { return ErrUnsigned }

// VerifyAuthenticode: only Windows has Authenticode; files elsewhere count as unsigned
// (an error wrapping ErrUnsigned).
func VerifyAuthenticode(string) (Signer, error) { return Signer{}, ErrUnsigned }
