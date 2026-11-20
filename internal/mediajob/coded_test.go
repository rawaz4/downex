package mediajob

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"downex/internal/media"
)

// codedServer serves the playlists of testdata/coded-hls (a video CDN's master, video and
// audio playlists, trimmed and renamed) and the way it codes them: measured on 2026-11-18,
// one of the CDNs behind such a host answered a playlist request that carried a Range
// header with a 206 of the playlist's gzip-coded representation (Content-Encoding: gzip,
// Content-Range over the coded bytes, Content-Type application/x-mpegURL) although the
// request asked for Accept-Encoding: identity, while the same request without Range got
// the playlist itself. coding "" behaves like that CDN; "gzip", "deflate" or "br" codes
// every answer with that coding, with or without Range.
type codedServer struct {
	ts     *httptest.Server
	coding string

	mu        sync.Mutex
	ranges    []string // Range of every playlist request, in order
	encodings []string // Accept-Encoding of every playlist request
}

func newCodedServer(t *testing.T, coding string) *codedServer {
	t.Helper()
	s := &codedServer{coding: coding}
	s.ts = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.ts.Close)
	return s
}

func (s *codedServer) serve(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/v/")
	data, err := os.ReadFile(filepath.Join("testdata", "coded-hls", filepath.Base(name)))
	if err != nil || !strings.HasSuffix(name, ".m3u8") {
		http.NotFound(w, r)
		return
	}
	rng := r.Header.Get("Range")
	s.mu.Lock()
	s.ranges = append(s.ranges, rng)
	s.encodings = append(s.encodings, r.Header.Get("Accept-Encoding"))
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/x-mpegURL")
	w.Header().Set("Accept-Ranges", "bytes")
	coding := s.coding
	if coding == "" && rng != "" {
		coding = "gzip"
	}
	if coding != "" {
		data = codeBytes(coding, data)
		w.Header().Set("Content-Encoding", coding)
	}
	if rng == "bytes=0-" && s.coding == "" {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(data)-1, len(data)))
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(data)
		return
	}
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	_, _ = w.Write(data)
}

func (s *codedServer) seen() (ranges, encodings []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ranges...), append([]string(nil), s.encodings...)
}

// codeBytes applies coding to data; "br" stands for a coding the client cannot decode,
// so its bytes are only marked.
func codeBytes(coding string, data []byte) []byte {
	var b bytes.Buffer
	switch coding {
	case "gzip":
		zw := gzip.NewWriter(&b)
		_, _ = zw.Write(data)
		_ = zw.Close()
	case "deflate":
		zw := zlib.NewWriter(&b)
		_, _ = zw.Write(data)
		_ = zw.Close()
	default:
		b.WriteString("\x0b\x80coded\r")
		b.Write(data)
	}
	return b.Bytes()
}

// pageHeaders are request headers a page's player used, as the extension passes them.
func pageHeaders() http.Header {
	h := http.Header{}
	h.Set("Referer", "https://www.page.example/")
	h.Set("Origin", "https://www.page.example")
	h.Set("User-Agent", "Mozilla/5.0 (test)")
	h.Set("Accept", "*/*")
	h.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	h.Set("Sec-Fetch-Mode", "cors")
	return h
}

// Playlists are read whole: a request for one carries no Range header, so a CDN that
// answers range requests from a coded copy is never asked for one, and the probe sees
// the playlists themselves (docs/MEDIA-SPEC.md §6 step 3).
func TestPlaylistsAreFetchedWithoutRange(t *testing.T) {
	s := newCodedServer(t, "")
	sel := Selection{ManifestURL: s.ts.URL + "/v/clip.m3u8", Headers: pageHeaders()}
	info, err := ProbeMP4Playlists(testContext(t), testFactory(t), sel, nil)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if info.Format != "hls" || len(info.Tracks) != 3 {
		t.Fatalf("info = %+v", info)
	}
	ranges, encodings := s.seen()
	if len(ranges) < 3 {
		t.Fatalf("%d playlist requests, want the master and two media playlists", len(ranges))
	}
	for i := range ranges {
		if ranges[i] != "" || encodings[i] != "identity" {
			t.Errorf("request %d: Range %q, Accept-Encoding %q; want none and identity", i, ranges[i], encodings[i])
		}
	}
}

// A playlist that comes coded anyway is decoded when the coding is gzip or deflate
// (RFC 9110 §8.4.1), and refused as unsupported, never as a malformed playlist, when it
// is another coding.
func TestCodedPlaylists(t *testing.T) {
	for _, coding := range []string{"gzip", "deflate"} {
		s := newCodedServer(t, coding)
		info, err := ProbeMP4Playlists(testContext(t), testFactory(t), Selection{ManifestURL: s.ts.URL + "/v/clip.m3u8"}, nil)
		if err != nil {
			t.Fatalf("%s: %v", coding, err)
		}
		if info.Format != "hls" || len(info.Tracks) != 3 {
			t.Fatalf("%s: info = %+v", coding, info)
		}
	}
	s := newCodedServer(t, "br")
	_, err := ProbeMP4Playlists(testContext(t), testFactory(t), Selection{ManifestURL: s.ts.URL + "/v/clip.m3u8"}, nil)
	if !errors.Is(err, media.ErrUnsupported) || errors.Is(err, media.ErrMalformed) {
		t.Fatalf("br: err = %v, want media.ErrUnsupported", err)
	}
	if strings.Contains(err.Error(), s.ts.URL) {
		t.Errorf("error repeats the URL: %v", err)
	}
}

// A coded playlist may not grow past media.MaxManifestBytes when decoded.
func TestCodedPlaylistSizeBound(t *testing.T) {
	big := append([]byte("#EXTM3U\n"), bytes.Repeat([]byte("#\n"), media.MaxManifestBytes/2+1)...)
	coded := codeBytes("gzip", big)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-mpegURL")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(coded)
	}))
	defer ts.Close()
	_, err := ProbeMP4Playlists(testContext(t), testFactory(t), Selection{ManifestURL: ts.URL + "/big.m3u8"}, nil)
	if !errors.Is(err, media.ErrTooLarge) {
		t.Fatalf("err = %v, want media.ErrTooLarge", err)
	}
}

// Fragment bytes are written as they arrive, so a coded answer to a range request is
// refused rather than saved as media.
func TestCodedFragmentRefused(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Range", "bytes 0-3/4")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte{0x1f, 0x8b, 8, 0})
	}))
	defer ts.Close()
	f := testFactory(t).NewSlot()
	defer f.Close()
	var out bytes.Buffer
	_, n, err := fetchInto(context.Background(), f, fetchSpec{url: ts.URL + "/seg.cmfv", rng: &[2]int64{0, 3}}, &out, newCopier(nil, nil), nil)
	if !errors.Is(err, ErrContentCoded) || n != 0 || out.Len() != 0 {
		t.Fatalf("err = %v, %d bytes written; want ErrContentCoded and none", err, n)
	}
}
