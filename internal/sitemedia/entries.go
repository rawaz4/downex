package sitemedia

import (
	"fmt"
	"time"
)

// Item is one video of a page with several, as every UI lists it (docs/SITE-MEDIA-SPEC.md
// §3.12): the site's id of it (its identity: never its position), its title, its
// duration (0 = unknown) and its thumbnail ("" = none).
type Item struct {
	ID        string
	Title     string
	Duration  time.Duration
	Thumbnail string
}

// ErrItemGone refuses a request for an item the page no longer has (site_unavailable):
// Downex never takes another item in its place.
var ErrItemGone = fmt.Errorf("%w: this item is no longer on the page", ErrSiteUnavailable)

// Entry is the video of a resolved page that a request names (docs/SITE-MEDIA-SPEC.md
// §3.12), with the page's items when it has several videos.
type Entry struct {
	// Info is the chosen video: the page itself when it has one video, else one of its
	// entries. Offers and Select take it as they take a page.
	Info *Info
	// ID is the chosen item's id ("" for a page with one video).
	ID string
	// Items are the page's videos Downex can save, in the page's order (nil for a page with
	// one video); LeftOut counts its other items: Info.LeftOut and the entries without an
	// offer (no MP4 video, protected, members only, ...).
	Items   []Item
	LeftOut int
}

// ChooseEntry finds the video of info that id names (docs/SITE-MEDIA-SPEC.md §3.12);
// pageHost is Offers'.
//
// A page with one video is its own entry: id must be "" or its id, else ErrItemGone. Of
// a page with several videos, the items are the entries Offers has an offer for (a direct
// one or the manifest fallback); id "" takes the item whose id is the playlist's own,
// else the first item, else (no entry has an offer) the first entry, whose Offers then
// says why; any other id must be an item's, else ErrItemGone.
func ChooseEntry(info *Info, id, pageHost string) (*Entry, error) {
	if info == nil {
		return nil, fmt.Errorf("%w: no page", ErrSiteError)
	}
	if !info.Playlist {
		if id != "" && id != info.ID {
			return nil, ErrItemGone
		}
		return &Entry{Info: info}, nil
	}
	e := &Entry{LeftOut: info.LeftOut}
	var offered []*Info
	for _, v := range info.Entries {
		if _, err := Offers(v, pageHost); err != nil {
			e.LeftOut++
			continue
		}
		offered = append(offered, v)
		e.Items = append(e.Items, Item{ID: v.ID, Title: v.Title, Duration: v.Duration, Thumbnail: v.Thumbnail})
	}
	switch {
	case id != "":
		for _, v := range offered {
			if v.ID == id {
				e.Info = v
			}
		}
		if e.Info == nil {
			return nil, ErrItemGone
		}
	case len(offered) == 0:
		if len(info.Entries) == 0 {
			return nil, fmt.Errorf("%w: no video on this page", ErrSiteUnsupported)
		}
		e.Info = info.Entries[0]
	default:
		e.Info = offered[0]
		for _, v := range offered {
			if v.ID == info.ID {
				e.Info = v
				break
			}
		}
	}
	e.ID = e.Info.ID
	return e, nil
}
