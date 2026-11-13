package daemon

import (
	"encoding/json"
	"fmt"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/update"
)

// UpdateSettings are the update settings (docs/M6-SPEC.md §4; Settings fields 22–23,
// kept in the settings file as the "update" object).
type UpdateSettings struct {
	// FeedURL is the https URL of the signed update manifest (update.CheckFeedURL). An
	// empty address, read from the settings file or sent by a client, means the build's
	// built-in feed (update.DefaultFeed), so FeedURL is "" only in a build without one,
	// where updates are off. The user turns updates off with AutoCheck, not by clearing
	// the address. The settings file keeps the built-in feed as "" (MarshalJSON), so
	// that a later build's feed replaces it.
	FeedURL string `json:"feed_url"`
	// AutoCheck checks for updates a minute after the daemon starts and then every 24
	// hours, while FeedURL is set (default true).
	AutoCheck bool `json:"auto_check"`
}

// builtinFeed is the build's default feed (update.DefaultFeed): "" in a build without
// one, or with an invalid one (which StartUpdates logs).
func builtinFeed() string {
	feed, _ := update.DefaultFeed()
	return feed
}

// DefaultUpdateSettings are the update settings of a new installation (and of a
// settings file of an older daemon): the release feed built in (builtinFeed) and
// automatic checks on.
func DefaultUpdateSettings() UpdateSettings {
	return UpdateSettings{FeedURL: builtinFeed(), AutoCheck: true}
}

// withDefaultFeed returns u with an empty FeedURL replaced by the built-in feed.
func (u UpdateSettings) withDefaultFeed() UpdateSettings {
	if u.FeedURL == "" {
		u.FeedURL = builtinFeed()
	}
	return u
}

// UnmarshalJSON reads the settings file's "update" object: fields it lacks take their
// DefaultUpdateSettings values. A missing or empty feed_url is the built-in feed: every
// 0.9.x settings file that was ever saved has "feed_url":"", and those installations
// get the release feed too. Any other feed_url is the user's own and is kept.
func (u *UpdateSettings) UnmarshalJSON(data []byte) error {
	var f struct {
		FeedURL   *string `json:"feed_url"`
		AutoCheck *bool   `json:"auto_check"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	*u = DefaultUpdateSettings()
	if f.FeedURL != nil && *f.FeedURL != "" {
		u.FeedURL = *f.FeedURL
	}
	if f.AutoCheck != nil {
		u.AutoCheck = *f.AutoCheck
	}
	return nil
}

// MarshalJSON writes the settings file's "update" object, with the built-in feed as ""
// so that the file follows the feed of whichever build reads it next.
func (u UpdateSettings) MarshalJSON() ([]byte, error) {
	feed := u.FeedURL
	if feed == builtinFeed() {
		feed = ""
	}
	return json.Marshal(struct {
		FeedURL   string `json:"feed_url"`
		AutoCheck bool   `json:"auto_check"`
	}{feed, u.AutoCheck})
}

// normalize validates u as part of a full replacement from a client: the feed must be
// empty (the built-in feed, withDefaultFeed) or an https URL (update.CheckFeedURL). The
// error names the API field.
func (u UpdateSettings) normalize() (UpdateSettings, error) {
	u = u.withDefaultFeed()
	if u.FeedURL == "" {
		return u, nil
	}
	if _, err := update.CheckFeedURL(u.FeedURL); err != nil {
		return u, fmt.Errorf("update_feed_url: %w", err)
	}
	return u, nil
}

// repair replaces an invalid feed URL read from disk with the built-in feed (updates
// are off in a build without one) and describes what it replaced.
func (u UpdateSettings) repair() (UpdateSettings, []string) {
	n, err := u.normalize()
	if err != nil {
		n = UpdateSettings{FeedURL: builtinFeed(), AutoCheck: u.AutoCheck}
		what := "; using the built-in update feed"
		if n.FeedURL == "" {
			what = "; updates are off"
		}
		return n, []string{err.Error() + what}
	}
	return n, nil
}

// setProto writes u into the API settings p (fields 22–23).
func (u UpdateSettings) setProto(p *downexv1.Settings) {
	p.UpdateFeedUrl = u.FeedURL
	p.AutoCheckUpdates = u.AutoCheck
}

// updateSettingsFromProto reads the update settings of API settings (a full
// replacement; an empty feed becomes the built-in one in normalize, which also
// validates).
func updateSettingsFromProto(p *downexv1.Settings) UpdateSettings {
	return UpdateSettings{FeedURL: p.GetUpdateFeedUrl(), AutoCheck: p.GetAutoCheckUpdates()}
}

// autoChecks reports whether automatic checks run.
func (u UpdateSettings) autoChecks() bool { return u.FeedURL != "" && u.AutoCheck }
