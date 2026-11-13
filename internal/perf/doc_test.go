//go:build perf

// Package perf is the download speed benchmark of Downex: it drives the real engine
// (internal/engine) and media job (internal/mediajob) exactly as the daemon configures
// them (cmd/downexd main.go and internal/daemon: the HTTP transport factory with the
// network runtime as proxy resolver, credentials and host rules, the global → queue →
// download limiter chain, an SQLite store, default_max_connections 8) against
//
//   - local scenarios: a loopback server that caps every TCP connection with a token
//     bucket and adds a round-trip time to each connection's handshakes and to each
//     request's first byte (netem_test.go), over TLS with HTTP/1.1 or HTTP/2; and
//   - real servers (opt-in: DOWNEX_PERF_REAL=1), with curl.exe as a single-connection
//     reference.
//
// Every file carries the "perf" build tag, so default builds and "go test ./..." never
// see it. Run it with
//
//	go test -tags perf -run TestPerf -timeout 0 -v ./internal/perf
//
// or compile a standalone binary (go test -c -tags perf -o perf.exe ./internal/perf)
// and run "perf.exe -test.run TestPerf -test.v". Settings come from the environment
// (see config in perf_test.go); results are written as <label>.json and <label>.md.
package perf
