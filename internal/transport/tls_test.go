package transport

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"testing"

	"downex/internal/model"
)

func TestTLSVerificationFailures(t *testing.T) {
	for _, kind := range []string{kindHTTP1TLS, kindHTTP2TLS} {
		t.Run(kind, func(t *testing.T) {
			var hits atomic.Int32
			ts := startServer(t, kind, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
			}))

			t.Run("untrusted self-signed certificate", func(t *testing.T) {
				// System roots only: the httptest certificate is not trusted.
				f, err := NewHTTPFactory(DefaultHTTPOptions())
				if err != nil {
					t.Fatal(err)
				}
				slot := f.NewSlot()
				defer slot.Close()
				_, err = slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: ts.URL}, End: -1})
				if !errors.Is(err, ErrTLS) {
					t.Fatalf("err = %v, want ErrTLS", err)
				}
				var cve *tls.CertificateVerificationError
				if !errors.As(err, &cve) {
					t.Errorf("underlying *tls.CertificateVerificationError not kept in %v", err)
				}
			})

			t.Run("hostname mismatch", func(t *testing.T) {
				// The certificate is trusted but only valid for 127.0.0.1, ::1 and example.com.
				_, port, _ := net.SplitHostPort(ts.Listener.Addr().String())
				slot := newTestSlot(t, DefaultHTTPOptions(), ts)
				_, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: fmt.Sprintf("https://localhost:%s/", port)}, End: -1})
				if !errors.Is(err, ErrTLS) {
					t.Fatalf("err = %v, want ErrTLS", err)
				}
				var he x509.HostnameError
				if !errors.As(err, &he) {
					t.Errorf("underlying x509.HostnameError not kept in %v", err)
				}
			})

			if n := hits.Load(); n != 0 {
				t.Errorf("server handled %d requests over unverified TLS", n)
			}
		})
	}
}

func TestTLSMinimumVersion(t *testing.T) {
	ts := startServer(t, kindHTTP1TLS, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	// Restrict the server to TLS 1.1 before its first handshake; the client requires 1.2.
	ts.TLS.MinVersion = tls.VersionTLS10
	ts.TLS.MaxVersion = tls.VersionTLS11
	slot := newTestSlot(t, DefaultHTTPOptions(), ts)
	_, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: ts.URL}, End: -1})
	if err == nil {
		t.Fatal("connected to a TLS 1.1-only server")
	}
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		err  error
		want error
	}{
		{x509.UnknownAuthorityError{}, ErrTLS},
		{fmt.Errorf("wrapped: %w", x509.HostnameError{Host: "a"}), ErrTLS},
		{x509.CertificateInvalidError{Reason: x509.Expired}, ErrTLS},
		{&tls.CertificateVerificationError{Err: errors.New("x")}, ErrTLS},
		{errors.New("net/http: server response headers exceeded 65536 bytes; aborted"), ErrTooLarge},
		{errors.New("http2: response header list larger than advertised limit"), ErrTooLarge},
	}
	for _, tc := range tests {
		got := classifyError(tc.err)
		if !errors.Is(got, tc.want) {
			t.Errorf("classifyError(%v) = %v, want %v", tc.err, got, tc.want)
		}
		if !errors.Is(got, tc.err) {
			t.Errorf("classifyError(%v) lost the original error", tc.err)
		}
	}
	plain := errors.New("connection refused")
	if got := classifyError(plain); got != plain {
		t.Errorf("classifyError(plain) = %v", got)
	}
}
