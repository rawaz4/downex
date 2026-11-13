package daemon

import (
	"encoding/json"
	"fmt"

	downexv1 "downex/internal/rpc/downex/v1"
)

// yt-dlp release channels (docs/SITE-MEDIA-SPEC.md §4.5).
const (
	ToolsChannelStable  = "stable"
	ToolsChannelNightly = "nightly"
	// DefaultToolsChannel is the channel of a new installation (and of "" from a client):
	// nightly, which upstream recommends for regular users (§9).
	DefaultToolsChannel = ToolsChannelNightly
)

// MediaSettings are the site-media settings (docs/SITE-MEDIA-SPEC.md §3.8; Settings fields
// 24–26, kept in the settings file as the "media" object). A settings file without them
// (or without some of them) gets DefaultMediaSettings' values for what is missing.
type MediaSettings struct {
	// SiteMedia resolves video pages with yt-dlp, which is downloaded on first use
	// (default on).
	SiteMedia bool `json:"site_media"`
	// ToolsAutoUpdate checks daily for newer releases of Downex's own copies of yt-dlp and
	// Deno (default on); copies found on PATH are never updated.
	ToolsAutoUpdate bool `json:"tools_auto_update"`
	// ToolsChannel is the yt-dlp release channel, ToolsChannelStable or
	// ToolsChannelNightly; "" (from a client that sent none) reads as DefaultToolsChannel.
	ToolsChannel string `json:"tools_channel"`
}

// DefaultMediaSettings returns the site-media settings of a new installation.
func DefaultMediaSettings() MediaSettings {
	return MediaSettings{SiteMedia: true, ToolsAutoUpdate: true, ToolsChannel: DefaultToolsChannel}
}

// UnmarshalJSON decodes the settings file's "media" object: fields it lacks keep their
// DefaultMediaSettings values.
func (s *MediaSettings) UnmarshalJSON(data []byte) error {
	type plain MediaSettings // without this method
	v := plain(DefaultMediaSettings())
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*s = MediaSettings(v)
	return nil
}

// normalize validates s as part of a full replacement from a client: the channel must be
// stable, nightly or "" (the default). The error names the API field.
func (s MediaSettings) normalize() (MediaSettings, error) {
	switch s.ToolsChannel {
	case "", ToolsChannelStable, ToolsChannelNightly:
	default:
		return s, fmt.Errorf("tools_channel must be %q or %q, got %q", ToolsChannelStable, ToolsChannelNightly,
			truncate(s.ToolsChannel, 32))
	}
	return s, nil
}

// repair replaces an unknown channel read from disk with the default and describes the
// replacement.
func (s MediaSettings) repair() (MediaSettings, []string) {
	n, err := s.normalize()
	if err != nil {
		s.ToolsChannel = DefaultToolsChannel
		return s, []string{fmt.Sprintf("%v; using %q", err, DefaultToolsChannel)}
	}
	return n, nil
}

// channel returns the yt-dlp channel in effect.
func (s MediaSettings) channel() string {
	if s.ToolsChannel == "" {
		return DefaultToolsChannel
	}
	return s.ToolsChannel
}

// toProto sets the site-media fields (24–26) of p.
func (s MediaSettings) toProto(p *downexv1.Settings) {
	p.SiteMedia = s.SiteMedia
	p.ToolsAutoUpdate = s.ToolsAutoUpdate
	p.ToolsChannel = s.ToolsChannel
}

// mediaSettingsFromProto reads the site-media fields (24–26) of API settings (a full
// replacement; validation happens in normalize).
func mediaSettingsFromProto(p *downexv1.Settings) MediaSettings {
	return MediaSettings{
		SiteMedia:       p.GetSiteMedia(),
		ToolsAutoUpdate: p.GetToolsAutoUpdate(),
		ToolsChannel:    p.GetToolsChannel(),
	}
}
