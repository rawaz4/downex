// Package played saves the video the user's browser played (docs/SITE-MEDIA-SPEC.md
// §2.9, "Save what's playing"): the extension hands over the files the tab's own player
// fetched, each with its first bytes, and the clicked <video>'s duration, picture and
// sound. This package checks that source, describes each file from its ISO-BMFF head
// (ISO/IEC 14496-12), keeps the files whose duration is the element's, and offers them as
// one video: a muxed file, or a video file and an audio file to join. It is pure: no
// request, no clock, no state.
package played

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"downex/internal/transport"
)

// OriginPlayed is model.Media.Origin of a played video.
const OriginPlayed = "played"

// Limits of a source (docs/SITE-MEDIA-SPEC.md §2.9 "Limits").
const (
	MaxResources   = 16
	MaxURLBytes    = 8 << 10
	MaxHeadBytes   = 256 << 10
	MaxHeadsTotal  = 1 << 20
	MaxSize        = 1 << 40
	MaxDuration    = 24 * time.Hour
	MaxPictureSide = 16384
)

// Resource is one file the tab's player fetched.
type Resource struct {
	ID   string
	URL  string // the whole file's address
	Size int64  // exact total, 0 = unknown
	// Kind is the extension's hint ("video", "audio", ""); the head decides.
	Kind string
	Head []byte
	// Browser: the head needed the tab, so the file is fetched through the browser.
	Browser bool
}

// Element is what the clicked <video> says of itself.
type Element struct {
	Duration      time.Duration // 0 = unknown
	Width, Height int           // 0 = unknown
	// HasAudio is 1 (it has sound), 0 (unknown) or -1 (none).
	HasAudio int
	// Others are the durations of the frame's other <video> elements (at most MaxOthers): a
	// file closer to one of them than to this element's duration is that element's.
	Others []time.Duration
}

// MaxOthers is how many other elements' durations a source carries at most.
const MaxOthers = 8

// Source is a validated played source.
type Source struct {
	Resources []Resource
	Element   Element
}

// Errors. ErrInvalid is a source outside the limits (InvalidArgument); ErrNotFound means
// no file matches the element yet (the panel keeps waiting; reason NotFoundYet).
var (
	ErrInvalid  = errors.New("played: invalid source")
	ErrNotFound = errors.New("played: no file of this video was played yet")
	// ErrRefusedHost is an address on a host Downex never fetches a played file from
	// (YouTube's, this computer's, a local network's): such a file is left out of the
	// source, never the source refused for it (a YouTube embed's file in the same tab).
	ErrRefusedHost = errors.New("played: a host whose files are never saved this way")
)

// NotFoundYet is the reason of ErrNotFound, sent as the error's code reason.
const NotFoundYet = "not_found_yet"

// Reasons of a played run's failures (docs/SITE-MEDIA-SPEC.md §2.9 "Errors"); none names a
// sign-in.
const (
	ReasonBrowserOnly = "The site answers this video only to your browser: download it with Downex's button in your browser."
	ReasonExpired     = "This video's link expired. Click Download on the video again."
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Check validates a source's limits: 1..MaxResources resources with unique ids "r1".."r99",
// addresses (CheckURL; a file on a refused host is kept out by Match, and at least one
// file must be on another host), sizes, heads, the element's duration and picture.
func Check(src Source) error {
	if n := len(src.Resources); n == 0 || n > MaxResources {
		return invalid("1 to %d files are needed, got %d", MaxResources, n)
	}
	seen := map[string]bool{}
	total, usable := 0, 0
	for i, r := range src.Resources {
		if !validID(r.ID) {
			return invalid("file %d: the id must be r1 to r99", i+1)
		}
		if seen[r.ID] {
			return invalid("file %d: id %q is used twice", i+1, r.ID)
		}
		seen[r.ID] = true
		switch err := CheckURL(r.URL); {
		case err == nil:
			usable++
		case !errors.Is(err, ErrRefusedHost):
			return invalid("file %d: %v", i+1, err)
		}
		if r.Size < 0 || r.Size > MaxSize {
			return invalid("file %d: size out of range", i+1)
		}
		switch r.Kind {
		case "", "video", "audio":
		default:
			return invalid("file %d: kind must be video, audio or empty", i+1)
		}
		if len(r.Head) == 0 || len(r.Head) > MaxHeadBytes {
			return invalid("file %d: the head must be 1 to %d bytes", i+1, MaxHeadBytes)
		}
		total += len(r.Head)
	}
	if usable == 0 {
		return invalid("no file is on a host Downex saves played videos from")
	}
	if total > MaxHeadsTotal {
		return invalid("the heads are %d bytes, more than %d", total, MaxHeadsTotal)
	}
	e := src.Element
	if e.Duration < 0 || e.Duration > MaxDuration {
		return invalid("the element's duration is out of range")
	}
	if e.Width < 0 || e.Height < 0 || e.Width > MaxPictureSide || e.Height > MaxPictureSide {
		return invalid("the element's picture is out of range")
	}
	if e.HasAudio < -1 || e.HasAudio > 1 {
		return invalid("has_audio must be -1, 0 or 1")
	}
	if len(e.Others) > MaxOthers {
		return invalid("at most %d other elements' durations", MaxOthers)
	}
	for _, d := range e.Others {
		if d <= 0 || d > MaxDuration {
			return invalid("another element's duration is out of range")
		}
	}
	return nil
}

func validID(id string) bool {
	if len(id) < 2 || len(id) > 3 || id[0] != 'r' || id[1] == '0' {
		return false
	}
	for _, c := range id[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// CheckURL accepts a played file's address: absolute http(s) with a host, at most
// MaxURLBytes, without user information or a fragment, not on a YouTube host, and not a
// loopback, private, link-local or unspecified literal address (nor "localhost"). The
// last three are ErrRefusedHost: Check keeps the source and Match leaves the file out.
func CheckURL(raw string) error {
	if raw == "" || len(raw) > MaxURLBytes {
		return fmt.Errorf("the address must be 1 to %d bytes", MaxURLBytes)
	}
	if strings.ContainsFunc(raw, func(r rune) bool { return r < 0x21 || r == 0x7f }) {
		return errors.New("the address has spaces or control characters")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("the address does not parse")
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return errors.New("the address is not http or https")
	case u.Host == "" || u.Hostname() == "":
		return errors.New("the address has no host")
	case u.User != nil:
		return errors.New("the address has user information")
	case u.Fragment != "" || strings.Contains(raw, "#"):
		return errors.New("the address has a fragment")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if YouTubeHost(host) {
		return fmt.Errorf("%w: YouTube's videos are not saved this way", ErrRefusedHost)
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return fmt.Errorf("%w: the address is this computer", ErrRefusedHost)
	}
	if ip, err := netip.ParseAddr(host); err == nil && transport.LocalAddress(ip) {
		return fmt.Errorf("%w: the address is a local network address", ErrRefusedHost)
	}
	return nil
}

// youtubeDomains are YouTube's hosts and its video servers': a played source there is
// refused (YouTube keeps its own path, docs/SITE-MEDIA-SPEC.md §2.9 "Never").
var youtubeDomains = []string{"youtube.com", "youtu.be", "youtube-nocookie.com", "googlevideo.com", "ytimg.com"}

// YouTubeHost reports a host of YouTube (or a subdomain of one).
func YouTubeHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, d := range youtubeDomains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// siteNames are the display names of the sites whose videos are often saved this way, by
// domain (the frame's host or a subdomain of it).
var siteNames = []struct{ domain, name string }{
	{"tiktok.com", "TikTok"}, {"instagram.com", "Instagram"}, {"facebook.com", "Facebook"},
	{"fb.watch", "Facebook"}, {"x.com", "X"}, {"twitter.com", "X"}, {"reddit.com", "Reddit"},
	{"pinterest.com", "Pinterest"}, {"threads.net", "Threads"}, {"threads.com", "Threads"},
	{"linkedin.com", "LinkedIn"}, {"bsky.app", "Bluesky"}, {"tumblr.com", "Tumblr"}, {"vk.com", "VK"},
	{"vkvideo.ru", "VK"}, {"vk.ru", "VK"}, {"9gag.com", "9GAG"}, {"imgur.com", "Imgur"},
	{"snapchat.com", "Snapchat"}, {"vimeo.com", "Vimeo"}, {"dailymotion.com", "Dailymotion"},
	{"twitch.tv", "Twitch"}, {"kick.com", "Kick"}, {"rumble.com", "Rumble"}, {"bilibili.com", "Bilibili"},
	{"streamable.com", "Streamable"}, {"telegram.org", "Telegram"}, {"weibo.com", "Weibo"},
}

// SiteName is the display name of a page's host: a known site's name, else the host
// without "www." ("" for an address that does not parse).
func SiteName(pageURL string) string {
	u, err := url.Parse(pageURL)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	for _, s := range siteNames {
		if host == s.domain || strings.HasSuffix(host, "."+s.domain) {
			return s.name
		}
	}
	host = strings.TrimPrefix(host, "www.")
	if len(host) > 64 {
		return ""
	}
	return host
}
