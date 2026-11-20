package scene

import (
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/state"
)

// Expired links (docs/DESKTOP-SPEC.md §10) in the simulated daemon: the fixture token
// "renew" adds, for the demo and the fit and render tests:
//   - dataset-2026.tar.zst: a download whose link expired at 1.7 GB of 4.2 GB, with the
//     page it came from ("Link expired", New link…, Open page);
//   - nightly-build.zip: a hand-off whose link downexd is checking ("Checking link…");
//   - dataset-2026 (1).tar.zst: a hand-off awaiting confirmation that offers to continue
//     the first ("Same file as a stopped download").

// FixtureRenewNames are the downloads the "renew" token adds, in the order above.
var FixtureRenewNames = []string{"dataset-2026.tar.zst", "nightly-build.zip", "dataset-2026 (1).tar.zst"}

// applyRenewFixture adds the downloads of the "renew" token.
func (m *Mock) applyRenewFixture() error {
	old, err := m.addNow(&downexv1.AddRequest{Url: "https://cdn.data.example.org/releases/dataset-2026.tar.zst?sig=a1",
		Filename: "dataset-2026.tar.zst", Source: srcBrowser, StartPaused: true, PageUrl: "https://data.example.org/releases"})
	if err != nil {
		return err
	}
	s := m.sims[old.GetId()]
	if s == nil {
		return fixtureError("the expired download was not added")
	}
	d := s.d
	d.state, d.mode = state.StateFailed, downexv1.Mode_MODE_RANGED
	d.size, d.received = 4_200_000_000, 1_700_000_000
	d.errCode = "link_expired"
	d.err = "connection 3: bytes 1700000000-2099999999: HTTP 403 Forbidden: engine: link expired"
	d.pageURL = "https://data.example.org/releases"
	m.touch(d)

	held, err := m.addNow(&downexv1.AddRequest{Url: "https://builds.example.net/nightly-build.zip?token=7",
		Filename: "nightly-build.zip", Source: srcBrowser, ExpectedSize: 812_000_000})
	if err != nil {
		return err
	}
	if s := m.sims[held.GetId()]; s != nil {
		s.d.state, s.d.renewCheck = state.StateAwaiting, true
		m.touch(s.d)
	}

	offer, err := m.addNow(&downexv1.AddRequest{Url: "https://cdn.data.example.org/releases/dataset-2026.tar.zst?sig=b7",
		Filename: "dataset-2026 (1).tar.zst", Source: srcBrowser, ExpectedSize: 4_200_000_000})
	if err != nil {
		return err
	}
	if s := m.sims[offer.GetId()]; s != nil {
		s.d.state = state.StateAwaiting
		s.d.renewOffer = &downexv1.RenewOffer{DownloadId: d.id, Filename: d.filename, Size: int64(d.size),
			Received: int64(d.received)}
		m.touch(s.d)
	}
	return nil
}
