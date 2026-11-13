package progress

import (
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/media"
)

// What progress uses of ui/core/media (lib/media.ts).

func isSiteDownload(d *downexv1.Download) bool       { return media.IsSiteDownload(d) }
func siteVideoLabel(info *downexv1.MediaInfo) string { return media.SiteVideoLabel(info) }
func formatLabel(format string) string               { return media.FormatLabel(format) }
func containerLabel(container string) string         { return media.ContainerLabel(container) }
func cannotRetry(code string) bool                   { return media.CannotRetry(code) }

// codecSummary is the chosen tracks' quality in short (media.ts codecSummary without a pick).
func codecSummary(info *downexv1.MediaInfo) string { return media.CodecSummary(info, nil) }

// chosenTracks is the chosen video and audio tracks in words (media.ts chosenTracks).
func chosenTracks(info *downexv1.MediaInfo) (video, audio string) { return media.ChosenTracks(info) }
