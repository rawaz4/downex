package transport

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
)

// classifyError wraps err with ErrTLS or ErrTooLarge when it is one of those failures,
// keeping the original error in the chain for errors.As.
func classifyError(err error) error {
	switch {
	case isTLSVerificationError(err):
		return fmt.Errorf("%w: %w", ErrTLS, err)
	case isHeaderTooLarge(err):
		return fmt.Errorf("%w: %w", ErrTooLarge, err)
	}
	return err
}

// isTLSVerificationError reports certificate or hostname verification failures.
func isTLSVerificationError(err error) bool {
	var (
		verification *tls.CertificateVerificationError
		hostname     x509.HostnameError
		authority    x509.UnknownAuthorityError
		invalid      x509.CertificateInvalidError
		roots        x509.SystemRootsError
		constraint   x509.ConstraintViolationError
		algorithm    x509.InsecureAlgorithmError
	)
	return errors.As(err, &verification) ||
		errors.As(err, &hostname) ||
		errors.As(err, &authority) ||
		errors.As(err, &invalid) ||
		errors.As(err, &roots) ||
		errors.As(err, &constraint) ||
		errors.As(err, &algorithm)
}

// isHeaderTooLarge reports a response whose headers exceeded MaxHeaderBytes. net/http
// has no exported sentinel for this, so the HTTP/1 and HTTP/2 messages are matched.
func isHeaderTooLarge(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "server response headers exceeded") ||
		strings.Contains(msg, "response header list larger than advertised limit") ||
		strings.Contains(msg, "header list too large")
}
