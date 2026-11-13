package main

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"time"

	"downex/internal/daemon"
	"downex/internal/update"
)

// updateClient returns the client of update checks and downloads (docs/M6-SPEC.md §4):
// it connects through the proxy of the network settings (network.ProxyURL) and trusts
// roots instead of the system's root certificates when they are given (the end-to-end
// tests' build only).
func updateClient(network *daemon.NetworkRuntime, roots *x509.CertPool) *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = network.ProxyURL
	t.ResponseHeaderTimeout = 30 * time.Second
	if roots != nil {
		t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	}
	return update.NewHTTPClient(t)
}
