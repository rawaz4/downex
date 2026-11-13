// Package ipc provides the per-user local endpoint between downexd and its clients
// (CLI, desktop UI, native-messaging relay): a Windows named pipe or a unix socket
// (Linux, macOS), both restricted to the current user, carrying HTTP/2 cleartext
// (Connect RPC).
package ipc

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

// BaseURL is the base URL for Connect clients built on NewHTTPClient.
// The host part is ignored; every request is dialled through the local endpoint.
const BaseURL = "http://downexd"

// EnvEndpoint overrides the endpoint returned by Endpoint (used by tests and by
// multi-profile setups). On Windows it is a pipe path (\\.\pipe\name); on Linux and
// macOS an absolute socket path (at most 103 bytes on macOS).
const EnvEndpoint = "DOWNEX_ENDPOINT"

var (
	// ErrAlreadyRunning means another daemon of this user holds the instance lock or endpoint.
	ErrAlreadyRunning = errors.New("downexd is already running")
	// ErrNotRunning means no daemon is listening on the endpoint.
	ErrNotRunning = errors.New("downexd is not running")
	// ErrForeignEndpoint means the endpoint (or the instance lock) exists but is held by
	// something that is not a downexd of the current user at the caller's trust level:
	// another account, or a lower-integrity (sandboxed) process of the same user. Such
	// an endpoint is never used. Errors wrapping it also wrap fs.ErrPermission.
	ErrForeignEndpoint = errors.New("the downexd endpoint is held by a foreign process")
)

// The platform-specific functions are (see ipc_<os>.go):
//
//	Endpoint() (string, error)                              per-user default endpoint, honouring EnvEndpoint
//	Listen(endpoint string) (net.Listener, error)           user-only ACL; ErrAlreadyRunning if in use
//	Dial(ctx context.Context, endpoint string) (net.Conn, error)   ErrNotRunning when nothing listens,
//	                                                        ErrForeignEndpoint for an impostor
//	AcquireInstanceLock(endpoint string) (release func(), err error)  one daemon per user/endpoint;
//	                                                        ErrAlreadyRunning, or ErrForeignEndpoint when squatted

// NewHTTPClient returns an HTTP client that speaks HTTP/2 cleartext over Dial(endpoint).
func NewHTTPClient(endpoint string) *http.Client {
	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	return &http.Client{Transport: &http.Transport{
		Protocols: &p,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return Dial(ctx, endpoint)
		},
		MaxIdleConns:    4,
		IdleConnTimeout: 90 * time.Second,
	}}
}

// NewServer returns an HTTP server for h that accepts HTTP/2 cleartext (and HTTP/1.1 for
// debugging), with conservative header limits. Serve it on a Listener from Listen.
func NewServer(h http.Handler) *http.Server {
	var p http.Protocols
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	return &http.Server{
		Handler:           h,
		Protocols:         &p,
		ReadHeaderTimeout: 10 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
}
