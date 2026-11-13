// Package e2e holds the real-process end-to-end tests of M2 and M3
// (docs/DAEMON-SPEC.md, docs/BROWSER-SPEC.md §2): they build the downex, downexd and
// downex-nmhost executables, start downexd on a private endpoint with a temporary
// database and drive it through the downex CLI binary, or through downex-nmhost spoken
// to over piped stdio as a browser does, against an httptest range server
// (internal/testserver).
//
// The tests are skipped with -short. Under -race the executables are built with the
// race detector too, and a race reported by any child process fails the test.
package e2e
