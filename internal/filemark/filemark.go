// Package filemark records where a downloaded file came from, the way the operating
// system expects downloads to be marked (docs/M6-SPEC.md §3, mark_downloaded_files):
//
//   - Windows: the Zone.Identifier alternate data stream ("Mark of the Web":
//     [ZoneTransfer] ZoneId=3, ReferrerUrl, HostUrl), so that the shell, SmartScreen
//     and Office treat the file as coming from the Internet.
//   - macOS: the com.apple.quarantine attribute ("0081;<hex time>;Downex;<UUID>"),
//     which makes Gatekeeper check the file at its first open, and
//     com.apple.metadata:kMDItemWhereFroms (a binary property list of the URL and
//     the referrer), which Finder shows as "Where from".
//   - Linux: the user.xdg.origin.url and user.xdg.referrer.url attributes.
//
// URLs are recorded without their user information (credentials); the rest of the
// URL is kept. Marking can fail where the file system has no streams or extended
// attributes (FAT32, some network shares): the caller logs the error, the download
// stays complete. The formats are built by the OS-independent functions of this file.
package filemark

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf16"
)

// Agent is the application name recorded in the quarantine attribute.
const Agent = "Downex"

// Attribute and stream names.
const (
	// ZoneStream is the alternate data stream of the Windows mark.
	ZoneStream = "Zone.Identifier"
	// XattrQuarantine and XattrWhereFroms are the macOS attributes.
	XattrQuarantine = "com.apple.quarantine"
	XattrWhereFroms = "com.apple.metadata:kMDItemWhereFroms"
	// XattrOriginURL and XattrReferrerURL are the Linux (freedesktop) attributes.
	XattrOriginURL   = "user.xdg.origin.url"
	XattrReferrerURL = "user.xdg.referrer.url"
)

// zoneInternet is URLZONE_INTERNET.
const zoneInternet = 3

// quarantineFlags marks a file downloaded by an application that is not itself
// quarantine-aware about launching it: Gatekeeper checks it at the first open.
const quarantineFlags = "0081"

// ErrNotSupported is returned by Mark where files cannot be marked.
var ErrNotSupported = errors.New("filemark: marking files is not supported on this system")

// Origin is where a downloaded file came from.
type Origin struct {
	URL      string    // the downloaded URL (after redirects when known)
	Referrer string    // the page that linked to it; "" = none
	Time     time.Time // when it was downloaded (zero = now)
}

// Mark records o on the file at path with the platform's mark (see the package
// documentation). URLs that cannot be parsed are left out; an origin without any URL
// is still marked as coming from the Internet.
func Mark(path string, o Origin) error {
	if o.Time.IsZero() {
		o.Time = time.Now()
	}
	o.URL, o.Referrer = CleanURL(o.URL), CleanURL(o.Referrer)
	return markPlatform(path, o)
}

// checkRegular refuses to mark anything but an existing regular file.
func checkRegular(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("filemark: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("filemark: %s is not a regular file", path)
	}
	return nil
}

// CleanURL returns rawURL without user information, "" when it is empty, cannot be
// parsed or holds control characters (which could break the formats).
func CleanURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	u.User = nil
	s := u.String()
	if strings.ContainsFunc(s, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return ""
	}
	return s
}

// ZoneIdentifier returns the content of the Zone.Identifier stream for o (whose URLs
// are already cleaned): Internet zone, then ReferrerUrl and HostUrl when known.
func ZoneIdentifier(o Origin) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "[ZoneTransfer]\r\nZoneId=%d\r\n", zoneInternet)
	if o.Referrer != "" {
		b.WriteString("ReferrerUrl=" + o.Referrer + "\r\n")
	}
	if o.URL != "" {
		b.WriteString("HostUrl=" + o.URL + "\r\n")
	}
	return []byte(b.String())
}

// QuarantineValue returns the com.apple.quarantine value of a file downloaded at t
// with event id id: "0081;<seconds since 1970, hex>;Downex;<UUID>".
func QuarantineValue(t time.Time, id [16]byte) string {
	return fmt.Sprintf("%s;%08x;%s;%s", quarantineFlags, max(t.Unix(), 0), Agent, formatUUID(id))
}

// newEventID returns a random (version 4) UUID.
func newEventID() [16]byte {
	var id [16]byte
	_, _ = rand.Read(id[:]) // crypto/rand.Read never fails (Go 1.24+)
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	return id
}

// formatUUID formats id in the upper-case form macOS uses.
func formatUUID(id [16]byte) string {
	return fmt.Sprintf("%X-%X-%X-%X-%X", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16])
}

// whereFroms lists the URLs of o for kMDItemWhereFroms: the URL, then the referrer.
func whereFroms(o Origin) []string {
	var list []string
	for _, s := range []string{o.URL, o.Referrer} {
		if s != "" {
			list = append(list, s)
		}
	}
	return list
}

// StringArrayPlist encodes items as a binary property list ("bplist00") holding one
// array of strings, the format of kMDItemWhereFroms. Strings of 7-bit ASCII are
// stored as ASCII, others as UTF-16 (big-endian); equal strings are stored once.
func StringArrayPlist(items []string) []byte {
	// Objects: 0 = the array, then each distinct string in order of appearance.
	index := make(map[string]int, len(items))
	var strs []string
	refs := make([]int, len(items))
	for i, s := range items {
		n, ok := index[s]
		if !ok {
			strs = append(strs, s)
			n = len(strs)
			index[s] = n
		}
		refs[i] = n
	}
	numObjects := 1 + len(strs)
	refSize := intSize(uint64(numObjects))

	buf := []byte("bplist00")
	offsets := make([]uint64, 0, numObjects)
	offsets = append(offsets, uint64(len(buf)))
	buf = appendMarker(buf, 0xA0, len(items))
	for _, r := range refs {
		buf = appendUint(buf, uint64(r), refSize)
	}
	for _, s := range strs {
		offsets = append(offsets, uint64(len(buf)))
		if isASCII(s) {
			buf = appendMarker(buf, 0x50, len(s))
			buf = append(buf, s...)
			continue
		}
		units := utf16.Encode([]rune(s))
		buf = appendMarker(buf, 0x60, len(units))
		for _, u := range units {
			buf = binary.BigEndian.AppendUint16(buf, u)
		}
	}
	tableOffset := uint64(len(buf))
	offsetSize := intSize(tableOffset)
	for _, o := range offsets {
		buf = appendUint(buf, o, offsetSize)
	}
	// Trailer: 5 unused bytes, the sort version, the integer sizes, the object count,
	// the top object and where the offset table starts.
	buf = append(buf, 0, 0, 0, 0, 0, 0, byte(offsetSize), byte(refSize))
	buf = binary.BigEndian.AppendUint64(buf, uint64(numObjects))
	buf = binary.BigEndian.AppendUint64(buf, 0)
	buf = binary.BigEndian.AppendUint64(buf, tableOffset)
	return buf
}

// appendMarker appends an object marker of type token (high nibble) with count n:
// in the low nibble below 15, else as a following integer object.
func appendMarker(buf []byte, token byte, n int) []byte {
	if n < 15 {
		return append(buf, token|byte(n))
	}
	size := intSize(uint64(n))
	buf = append(buf, token|0x0f, 0x10|log2(size))
	return appendUint(buf, uint64(n), size)
}

// intSize is the number of bytes (1, 2, 4 or 8) that hold v.
func intSize(v uint64) int {
	switch {
	case v < 1<<8:
		return 1
	case v < 1<<16:
		return 2
	case v < 1<<32:
		return 4
	}
	return 8
}

func log2(size int) byte {
	switch size {
	case 1:
		return 0
	case 2:
		return 1
	case 4:
		return 2
	}
	return 3
}

// appendUint appends v big-endian in size bytes.
func appendUint(buf []byte, v uint64, size int) []byte {
	for i := size - 1; i >= 0; i-- {
		buf = append(buf, byte(v>>(8*i)))
	}
	return buf
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
