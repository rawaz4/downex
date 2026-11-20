//go:build !windows

package update

// VerifyAuthenticode: only Windows has Authenticode; files elsewhere count as unsigned
// (an error wrapping ErrUnsigned).
func VerifyAuthenticode(string) (Signer, error) { return Signer{}, ErrUnsigned }
