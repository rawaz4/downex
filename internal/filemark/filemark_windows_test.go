//go:build windows

package filemark

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// namedStreams reports whether the volume of dir supports alternate data streams.
func namedStreams(t *testing.T, dir string) bool {
	t.Helper()
	root := filepath.VolumeName(dir) + string(filepath.Separator)
	p, err := windows.UTF16PtrFromString(root)
	if err != nil {
		t.Fatal(err)
	}
	var flags uint32
	if err := windows.GetVolumeInformation(p, nil, 0, nil, nil, &flags, nil, 0); err != nil {
		t.Logf("GetVolumeInformation(%s): %v", root, err)
		return false
	}
	return flags&windows.FILE_NAMED_STREAMS != 0
}

func TestMarkZoneIdentifier(t *testing.T) {
	dir := t.TempDir()
	if !namedStreams(t, dir) {
		t.Skip("the temporary directory's volume has no named streams (not NTFS)")
	}
	path := filepath.Join(dir, "setup file.exe")
	if err := os.WriteFile(path, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := Origin{URL: "https://user:pw@dl.example.com/setup%20file.exe?sig=abc", Referrer: "https://example.com/downloads"}
	if err := Mark(path, o); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path + ":" + ZoneStream)
	if err != nil {
		t.Fatal(err)
	}
	want := "[ZoneTransfer]\r\nZoneId=3\r\nReferrerUrl=https://example.com/downloads\r\n" +
		"HostUrl=https://dl.example.com/setup%20file.exe?sig=abc\r\n"
	if string(got) != want {
		t.Fatalf("Zone.Identifier = %q, want %q", got, want)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "payload" {
		t.Fatalf("file content after marking = %q, %v", data, err)
	}
	// Marking again replaces the stream (no leftover of a longer old one).
	if err := Mark(path, Origin{URL: "https://a.example/x"}); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(path + ":" + ZoneStream)
	if err != nil {
		t.Fatal(err)
	}
	if want := "[ZoneTransfer]\r\nZoneId=3\r\nHostUrl=https://a.example/x\r\n"; string(got) != want {
		t.Fatalf("Zone.Identifier after a second mark = %q, want %q", got, want)
	}
}

func TestMarkRefusesNonFiles(t *testing.T) {
	dir := t.TempDir()
	if err := Mark(dir, Origin{URL: "https://example.com/"}); err == nil {
		t.Fatal("Mark of a directory succeeded")
	}
	if _, err := os.Stat(dir + ":" + ZoneStream); err == nil {
		t.Fatal("the directory got a Zone.Identifier stream")
	}
	missing := filepath.Join(dir, "missing.zip")
	if err := Mark(missing, Origin{URL: "https://example.com/"}); err == nil {
		t.Fatal("Mark of a missing file succeeded")
	}
	if _, err := os.Stat(missing); err == nil {
		t.Fatal("Mark created the missing file")
	}
}
