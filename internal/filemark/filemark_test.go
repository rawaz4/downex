package filemark

import (
	"bytes"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCleanURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"  ", ""},
		{"https://example.com/a.zip", "https://example.com/a.zip"},
		{"https://user:secret@example.com/a.zip?token=t#f", "https://example.com/a.zip?token=t#f"},
		{"https://user@example.com:8443/a.zip", "https://example.com:8443/a.zip"},
		{"ftp://u:p@files.example/x", "ftp://files.example/x"},
		{" https://example.com/x \n", "https://example.com/x"},
		{"https://example.com/a\r\nInjected=1", ""},
		{"https://example.com/%zz", ""},
		{"https://example.com/a b.zip", "https://example.com/a%20b.zip"},
	} {
		if got := CleanURL(tc.in); got != tc.want {
			t.Errorf("CleanURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestZoneIdentifier(t *testing.T) {
	for _, tc := range []struct {
		o    Origin
		want string
	}{
		{Origin{URL: "https://example.com/a.zip", Referrer: "https://example.com/page"},
			"[ZoneTransfer]\r\nZoneId=3\r\nReferrerUrl=https://example.com/page\r\nHostUrl=https://example.com/a.zip\r\n"},
		{Origin{URL: "https://example.com/a.zip"}, "[ZoneTransfer]\r\nZoneId=3\r\nHostUrl=https://example.com/a.zip\r\n"},
		{Origin{}, "[ZoneTransfer]\r\nZoneId=3\r\n"},
	} {
		if got := string(ZoneIdentifier(tc.o)); got != tc.want {
			t.Errorf("ZoneIdentifier(%+v) = %q, want %q", tc.o, got, tc.want)
		}
	}
}

func TestQuarantineValue(t *testing.T) {
	id := [16]byte{0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0x4d, 0xef, 0x81, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}
	got := QuarantineValue(time.Unix(0x6543210f, 999).UTC(), id)
	if want := "0081;6543210f;Downex;12345678-9ABC-4DEF-8123-456789ABCDEF"; got != want {
		t.Fatalf("QuarantineValue = %q, want %q", got, want)
	}
	if got := QuarantineValue(time.Unix(5, 0), id); !strings.HasPrefix(got, "0081;00000005;") {
		t.Fatalf("QuarantineValue of an early time = %q", got)
	}
	re := regexp.MustCompile(`^0081;[0-9a-f]{8};Downex;[0-9A-F]{8}-[0-9A-F]{4}-4[0-9A-F]{3}-[89AB][0-9A-F]{3}-[0-9A-F]{12}$`)
	seen := map[string]bool{}
	for range 50 {
		v := QuarantineValue(time.Now(), newEventID())
		if !re.MatchString(v) {
			t.Fatalf("QuarantineValue = %q, not of the expected form", v)
		}
		uuid := v[strings.LastIndexByte(v, ';')+1:]
		if seen[uuid] {
			t.Fatalf("event id %s repeated", uuid)
		}
		seen[uuid] = true
	}
}

func TestWhereFroms(t *testing.T) {
	for _, tc := range []struct {
		o    Origin
		want []string
	}{
		{Origin{URL: "u", Referrer: "r"}, []string{"u", "r"}},
		{Origin{URL: "u"}, []string{"u"}},
		{Origin{Referrer: "r"}, []string{"r"}},
		{Origin{}, nil},
	} {
		got := whereFroms(tc.o)
		if len(got) != len(tc.want) || strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("whereFroms(%+v) = %q, want %q", tc.o, got, tc.want)
		}
	}
}

// Goldens produced by an independent binary property list writer (CPython's
// plistlib, FMT_BINARY) for the same arrays.
func TestStringArrayPlistGolden(t *testing.T) {
	many := make([]string, 16)
	for i := range many {
		many[i] = "u" + strconv.Itoa(i)
	}
	long := "https://example.com/" + strings.Repeat("a", 300) + ".zip"
	for _, tc := range []struct {
		name  string
		items []string
		hex   string
	}{
		{"two", []string{"https://example.com/a.zip", "https://example.com/"},
			"62706c6973743030a201025f101968747470733a2f2f6578616d706c652e636f6d2f612e7a69705f101468747470733a2f2f6578616d706c652e636f6d2f080b27000000000000010100000000000000030000000000000000000000000000003e"},
		{"one", []string{"https://dl.example.org/files/setup.exe?v=2"},
			"62706c6973743030a1015f102a68747470733a2f2f646c2e6578616d706c652e6f72672f66696c65732f73657475702e6578653f763d32080a0000000000000101000000000000000200000000000000000000000000000037"},
		{"unicode", []string{"https://example.com/été.zip", "https://例え.jp/"},
			"62706c6973743030a201026f101b00680074007400700073003a002f002f006500780061006d0070006c0065002e0063006f006d002f00e9007400e9002e007a006900706e00680074007400700073003a002f002f4f8b3048002e006a0070002f080b440000000000000101000000000000000300000000000000000000000000000061"},
		{"long", []string{long, "https://example.com/ref"},
			"62706c6973743030a201025f110144" + hex.EncodeToString([]byte(long)) +
				"5f101768747470733a2f2f6578616d706c652e636f6d2f7265660008000b0153000000000000020100000000000000030000000000000000000000000000016d"},
		{"duplicates", []string{"https://example.com/x.zip", "https://example.com/x.zip"},
			"62706c6973743030a201015f101968747470733a2f2f6578616d706c652e636f6d2f782e7a6970080b0000000000000101000000000000000200000000000000000000000000000027"},
		{"length 14 and 15", []string{"abcdefghijklmn", "abcdefghijklmno"},
			"62706c6973743030a201025e6162636465666768696a6b6c6d6e5f100f6162636465666768696a6b6c6d6e6f080b1a000000000000010100000000000000030000000000000000000000000000002c"},
		{"16 items", many,
			"62706c6973743030af10100102030405060708090a0b0c0d0e0f10527530527531527532527533527534527535527536527537527538527539537531305375313153753132537531335375313453753135081b1e2124272a2d303336393d4145494d0000000000000101000000000000001100000000000000000000000000000051"},
	} {
		want, err := hex.DecodeString(tc.hex)
		if err != nil {
			t.Fatal(err)
		}
		if got := StringArrayPlist(tc.items); !bytes.Equal(got, want) {
			t.Errorf("%s: StringArrayPlist =\n%x\nwant\n%x", tc.name, got, want)
		}
	}
}
