package main

import (
	"crypto/x509"

	"downex/internal/daemon"
	"downex/internal/engine"
	"downex/internal/polite"
)

// testHooks replace parts of the system for the end-to-end tests of internal/e2e, which
// build a second downexd with "-tags e2etest" (testhooks_e2e.go reads them from the
// environment). Release builds never have any (testhooks_release.go): a zero testHooks
// changes nothing.
type testHooks struct {
	// clock times the queue schedules and post-queue countdowns (daemon.Config.Clock).
	clock engine.Clock
	// power records the post-queue power actions instead of performing them; it is given
	// to every daemon of such a build, not only to the user's own.
	power daemon.Power
	// roots are trusted instead of the system's root certificates (downloads and update
	// checks); verification itself is never turned off.
	roots *x509.CertPool
	// updateKey is the public key update manifests are signed with
	// (daemon.UpdatesConfig.PublicKey).
	updateKey string
	// toolsBase replaces https://github.com for tool downloads (tools.Config.ReleaseBase)
	// and lets this daemon download tools although it is not the user's own
	// (docs/SITE-MEDIA-SPEC.md §8.3).
	toolsBase string
	// ytdlpKey is the armored OpenPGP key that yt-dlp's checksum signatures must verify
	// with instead of the embedded one (tools.Config.YtDlpKeys); nil keeps the embedded key.
	ytdlpKey []byte
	// passEnv is the prefix of the variables passed on to yt-dlp and Deno
	// (tools.Config.PassEnvPrefix): the stand-in tools of the tests read theirs.
	passEnv string
	// politeProber answers Polite mode's probes instead of the network
	// (daemon.Config.PoliteProber; nil: the system's prober), and politeLoopback makes the
	// test servers on loopback addresses anchors (docs/POLITE-SPEC.md §9).
	politeProber   polite.Prober
	politeLoopback bool
}
