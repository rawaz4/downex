package display

import (
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/media"
)

// What display uses of ui/core/media (lib/media.ts).

func isSiteDownload(d *downexv1.Download) bool       { return media.IsSiteDownload(d) }
func isSiteInfo(info *downexv1.MediaInfo) bool       { return media.IsSite(info) }
func siteVideoLabel(info *downexv1.MediaInfo) string { return media.SiteVideoLabel(info) }
func formatLabel(format string) string               { return media.FormatLabel(format) }
func cannotRetry(d *downexv1.Download) bool          { return media.CannotRetryDownload(d) }
