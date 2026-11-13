package sitemedia

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode"
)

// sentinels lists every sentinel error of the package.
var sentinels = []error{
	ErrSiteUnsupported, ErrSiteUnavailable, ErrAuthRequired, ErrSiteError,
	ErrSiteResolving, ErrToolsInstalling, ErrToolsUnavailable, ErrSiteMediaOff,
}

// The sentinel texts are short user-facing phrases, and no text contains another, so a
// stored message maps back to exactly one of them.
func TestSentinelTextsAreDistinct(t *testing.T) {
	for i, a := range sentinels {
		msg := a.Error()
		switch {
		case msg == "", len(msg) > 200:
			t.Errorf("sentinel %d text %q: want 1..200 bytes", i, msg)
		case strings.Contains(msg, "://"):
			t.Errorf("sentinel %d text %q contains a URL", i, msg)
		case strings.TrimSpace(msg) != msg || strings.HasSuffix(msg, "."):
			t.Errorf("sentinel %d text %q: want no outer spaces and no final period", i, msg)
		}
		for _, r := range msg {
			if !unicode.IsPrint(r) {
				t.Errorf("sentinel %d text %q has the unprintable %U", i, msg, r)
			}
		}
		for j, b := range sentinels {
			if i != j && strings.Contains(msg, b.Error()) {
				t.Errorf("sentinel %d text %q contains sentinel %d text %q", i, msg, j, b.Error())
			}
		}
	}
}

// A wrapped sentinel is found by errors.Is, matches no other sentinel and keeps its
// text, which reads as the message after the error code.
func TestWrappedSentinels(t *testing.T) {
	for i, s := range sentinels {
		err := fmt.Errorf("probe: %w: %s", s, "reason")
		for j, other := range sentinels {
			if got := errors.Is(err, other); got != (i == j) {
				t.Errorf("errors.Is(wrapped sentinel %d, sentinel %d) = %v", i, j, got)
			}
		}
		if !strings.Contains(err.Error(), s.Error()) {
			t.Errorf("wrapped sentinel %d lost its text: %q", i, err)
		}
	}
	if got := fmt.Errorf("%w (34%%)", ErrToolsInstalling).Error(); got != "downloading yt-dlp and Deno (34%)" {
		t.Errorf("tools progress text = %q", got)
	}
	if got := ErrSiteMediaOff.Error(); got != "video downloads from web pages are turned off (Settings → Video sites)" {
		t.Errorf("site_media_off text = %q", got)
	}
}

// The manifest origin is the empty string that records written before site media
// have; the two origins differ.
func TestOrigins(t *testing.T) {
	if OriginManifest != "" || OriginSite != "site" {
		t.Fatalf("origins %q and %q, want \"\" and \"site\"", OriginManifest, OriginSite)
	}
}
