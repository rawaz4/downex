//go:build !windows

package transport

import "errors"

// newCurrentUserHandshake is unsupported off Windows: NTLM/Negotiate as the signed-in
// user needs SSPI. The caller falls back to a password login when one is available.
func newCurrentUserHandshake(scheme, host string) (handshake, error) {
	return nil, errors.New("transport: current-user authentication needs Windows")
}
