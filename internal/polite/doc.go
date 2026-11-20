// Package polite is Polite mode (docs/POLITE-SPEC.md): a delay-based controller that
// slows Downex's own downloads when they make the line laggy and lets them run at full
// speed again when the line is quiet. It is receive-side and needs no admin rights.
//
// It measures the line with small probes (ICMP echo, else a TCP connect that never sends
// a request) to a few servers Downex is already downloading from, the anchors (§2). Each
// anchor's base RTT is the minimum over ten one-minute buckets, the base history of
// RFC 6817 §2.4; the line delay d is the lower median, across the trusted anchors, of
// each anchor's minimum RTT over the last 1.5 s minus its base (§3). The controller (§4)
// turns d into one cap R for all downloads together, which the daemon applies as the
// "polite" level of its limiter chain global → polite → queue → download (§5).
//
// The pieces:
//   - Controller is pure: no clock, no goroutines. It holds the anchors, the byte meter
//     and the state machine, decides which probe to send next and the cap after every
//     tick. Tests drive it with virtual time.
//   - Monitor runs a Controller: one goroutine with a tick and a probe ticker on an
//     engine.Clock, at most one probe goroutine per anchor, and the Apply and Publish
//     callbacks, which it never calls under its own lock.
//   - Prober sends the probes (NewSystemProber: IcmpSendEcho2 on Windows, unprivileged
//     datagram ICMP on macOS and Linux, TCP everywhere).
//
// Nothing is stored and nothing leaves the machine except the probes themselves, which go
// only to addresses Downex already holds a live connection to (or its proxy), never to
// the local network and never while nothing downloads (§6). Addresses are never logged.
package polite
