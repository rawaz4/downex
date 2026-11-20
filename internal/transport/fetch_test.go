package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"downex/internal/model"
)

func TestFetchRanges(t *testing.T) {
	const size = 200_000
	for _, kind := range allKinds {
		t.Run(kind, func(t *testing.T) {
			cs := newContentServer(size)
			ts := startServer(t, kind, cs)
			slot := newTestSlot(t, DefaultHTTPOptions(), ts)
			url := ts.URL + "/files/archive.zip"

			tests := []struct {
				name       string
				start, end int64
				wantRange  string
				want       ContentRange
			}{
				{"probe", 0, -1, "bytes=0-", ContentRange{0, size - 1, size}},
				{"bounded", 100, 199, "bytes=100-199", ContentRange{100, 199, size}},
				{"single byte", 5, 5, "bytes=5-5", ContentRange{5, 5, size}},
				{"open tail", size - 10, -1, fmt.Sprintf("bytes=%d-", size-10), ContentRange{size - 10, size - 1, size}},
				{"end past size", size - 4, size + 100, fmt.Sprintf("bytes=%d-%d", size-4, size+100), ContentRange{size - 4, size - 1, size}},
			}
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: url}, Start: tc.start, End: tc.end})
					if err != nil {
						t.Fatalf("Fetch: %v", err)
					}
					body := readBody(t, resp)
					if resp.StatusCode != http.StatusPartialContent {
						t.Fatalf("status = %d, want 206", resp.StatusCode)
					}
					if resp.ContentRange == nil || *resp.ContentRange != tc.want {
						t.Fatalf("ContentRange = %+v, want %+v", resp.ContentRange, tc.want)
					}
					wantBody := cs.content[tc.want.Start : tc.want.End+1]
					if !bytes.Equal(body, wantBody) {
						t.Fatalf("body mismatch: got %d bytes, want %d", len(body), len(wantBody))
					}
					if resp.ContentLength != int64(len(wantBody)) {
						t.Errorf("ContentLength = %d, want %d", resp.ContentLength, len(wantBody))
					}
					if resp.FinalURL != url {
						t.Errorf("FinalURL = %q, want %q", resp.FinalURL, url)
					}
					if resp.ContentType != "application/zip" {
						t.Errorf("ContentType = %q", resp.ContentType)
					}
					if resp.AcceptRangesNone {
						t.Error("AcceptRangesNone = true")
					}
					wantV := model.Validators{ETag: cs.etag, StrongETag: true, LastModified: cs.modTime.Format(http.TimeFormat)}
					if resp.Validators != wantV {
						t.Errorf("Validators = %+v, want %+v", resp.Validators, wantV)
					}

					seen := cs.rec.last(t)
					if got := seen.Header.Get("Range"); got != tc.wantRange {
						t.Errorf("server saw Range %q, want %q", got, tc.wantRange)
					}
					if got := seen.Header.Get("Accept-Encoding"); got != "identity" {
						t.Errorf("server saw Accept-Encoding %q, want identity", got)
					}
					if got := seen.Header.Get("User-Agent"); got != DefaultUserAgent {
						t.Errorf("server saw User-Agent %q", got)
					}
					wantProto := 1
					if kind == kindHTTP2TLS {
						wantProto = 2
					}
					if seen.Proto != wantProto {
						t.Errorf("server saw HTTP/%d, want HTTP/%d", seen.Proto, wantProto)
					}
				})
			}
		})
	}
}

func TestFetchHTTP2Disabled(t *testing.T) {
	cs := newContentServer(1000)
	ts := startServer(t, kindHTTP2TLS, cs)
	opts := DefaultHTTPOptions()
	opts.EnableHTTP2 = false
	slot := newTestSlot(t, opts, ts)
	resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: ts.URL}, End: -1})
	if err != nil {
		t.Fatal(err)
	}
	readBody(t, resp)
	if p := cs.rec.last(t).Proto; p != 1 {
		t.Fatalf("server saw HTTP/%d, want HTTP/1 with EnableHTTP2=false", p)
	}
}

func TestFetchIfRange(t *testing.T) {
	const size = 5000
	for _, kind := range []string{kindHTTP1, kindHTTP2TLS} {
		t.Run(kind, func(t *testing.T) {
			cs := newContentServer(size)
			ts := startServer(t, kind, cs)
			slot := newTestSlot(t, DefaultHTTPOptions(), ts)

			tests := []struct {
				name       string
				ifRange    string
				wantStatus int
			}{
				{"matching strong etag", cs.etag, http.StatusPartialContent},
				{"changed etag", `"v2-def"`, http.StatusOK},
				{"matching last-modified", cs.modTime.Format(http.TimeFormat), http.StatusPartialContent},
				{"older last-modified", cs.modTime.Add(-time.Hour).Format(http.TimeFormat), http.StatusOK},
			}
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					resp, err := slot.Fetch(testCtx(t), RangeRequest{
						Request: model.Request{URL: ts.URL}, Start: 1000, End: 1999, IfRange: tc.ifRange,
					})
					if err != nil {
						t.Fatal(err)
					}
					body := readBody(t, resp)
					if resp.StatusCode != tc.wantStatus {
						t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
					}
					if got := cs.rec.last(t).Header.Get("If-Range"); got != tc.ifRange {
						t.Errorf("server saw If-Range %q, want %q", got, tc.ifRange)
					}
					switch tc.wantStatus {
					case http.StatusPartialContent:
						if !bytes.Equal(body, cs.content[1000:2000]) {
							t.Error("partial body mismatch")
						}
					case http.StatusOK:
						if resp.ContentRange != nil {
							t.Errorf("ContentRange = %+v on 200", resp.ContentRange)
						}
						if !bytes.Equal(body, cs.content) {
							t.Error("full body mismatch")
						}
					}
				})
			}

			t.Run("weak etag refused", func(t *testing.T) {
				before := len(cs.rec.all())
				_, err := slot.Fetch(testCtx(t), RangeRequest{
					Request: model.Request{URL: ts.URL}, Start: 10, End: 20, IfRange: `W/"v1-abc"`,
				})
				if !errors.Is(err, ErrInvalidRequest) {
					t.Fatalf("err = %v, want ErrInvalidRequest", err)
				}
				if len(cs.rec.all()) != before {
					t.Error("a request with a weak If-Range reached the server")
				}
			})
		})
	}
}

func TestFetchUnsatisfiableRange(t *testing.T) {
	const size = 1234
	for _, kind := range []string{kindHTTP1, kindHTTP2TLS} {
		t.Run(kind, func(t *testing.T) {
			cs := newContentServer(size)
			ts := startServer(t, kind, cs)
			slot := newTestSlot(t, DefaultHTTPOptions(), ts)
			resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: ts.URL}, Start: size + 10, End: -1})
			if err != nil {
				t.Fatal(err)
			}
			readBody(t, resp)
			if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
				t.Fatalf("status = %d, want 416", resp.StatusCode)
			}
			want := ContentRange{Start: -1, End: -1, Total: size}
			if resp.ContentRange == nil || *resp.ContentRange != want {
				t.Fatalf("ContentRange = %+v, want %+v", resp.ContentRange, want)
			}
		})
	}
}

func TestFetchServerWithoutRanges(t *testing.T) {
	content := testContent(3000)
	ts := startServer(t, kindHTTP1, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "none")
		w.Header().Set("ETag", `W/"weak-1"`)
		w.Header().Set("Content-Disposition", `attachment; filename="report.pdf"`)
		w.Header().Set("Content-Range", "bytes 0-9/3000") // meaningless on 200; must be ignored
		_, _ = w.Write(content)
	}))
	slot := newTestSlot(t, DefaultHTTPOptions(), ts)
	resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: ts.URL + "/dl?id=7"}, End: -1})
	if err != nil {
		t.Fatal(err)
	}
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, content) {
		t.Fatalf("status %d, %d bytes", resp.StatusCode, len(body))
	}
	if !resp.AcceptRangesNone {
		t.Error("AcceptRangesNone = false")
	}
	if resp.ContentRange != nil {
		t.Errorf("ContentRange = %+v on 200", resp.ContentRange)
	}
	if resp.Validators.ETag != `W/"weak-1"` || resp.Validators.StrongETag {
		t.Errorf("Validators = %+v, want weak etag", resp.Validators)
	}
	if resp.Validators.IfRange() != "" {
		t.Errorf("IfRange() = %q, want none for a weak tag without Last-Modified", resp.Validators.IfRange())
	}
	if resp.Filename != "report.pdf" || resp.FilenameControls {
		t.Errorf("Filename = %q, controls %v", resp.Filename, resp.FilenameControls)
	}
}

// TestFetchFilenameControls: a Content-Disposition name with a right-to-left override
// is sanitised, and the response says the override was there (DESKTOP-SPEC §9.5 N1).
func TestFetchFilenameControls(t *testing.T) {
	ts := startServer(t, kindHTTP1, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename*=UTF-8''invoice%E2%80%AEfdp.exe`)
		w.Header().Set("Content-Type", "Application/PDF; charset=binary")
		_, _ = w.Write([]byte("x"))
	}))
	slot := newTestSlot(t, DefaultHTTPOptions(), ts)
	resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: ts.URL + "/dl"}, End: -1})
	if err != nil {
		t.Fatal(err)
	}
	readBody(t, resp)
	if resp.Filename != "invoicefdp.exe" || !resp.FilenameControls || resp.ContentType != "application/pdf" {
		t.Fatalf("Filename %q controls %v type %q", resp.Filename, resp.FilenameControls, resp.ContentType)
	}
}

func TestParseValidators(t *testing.T) {
	tests := []struct {
		etag, lastModified string
		want               model.Validators
	}{
		{`"abc"`, "", model.Validators{ETag: `"abc"`, StrongETag: true}},
		{` "abc" `, "", model.Validators{ETag: `"abc"`, StrongETag: true}},
		{`""`, "", model.Validators{ETag: `""`, StrongETag: true}},
		{`W/"abc"`, "", model.Validators{ETag: `W/"abc"`}},
		{`w/"abc"`, "", model.Validators{ETag: `w/"abc"`}},
		{`abc`, "", model.Validators{ETag: `abc`}},
		{`"a"b"`, "", model.Validators{ETag: `"a"b"`}},
		{`"a b"`, "", model.Validators{ETag: `"a b"`}},
		{``, "Tue, 15 Nov 1994 08:12:31 GMT", model.Validators{LastModified: "Tue, 15 Nov 1994 08:12:31 GMT"}},
		{``, "Tuesday, 15-Nov-94 08:12:31 GMT", model.Validators{LastModified: "Tuesday, 15-Nov-94 08:12:31 GMT"}},
		{``, "yesterday", model.Validators{}},
	}
	for _, tc := range tests {
		h := http.Header{}
		if tc.etag != "" {
			h.Set("ETag", tc.etag)
		}
		if tc.lastModified != "" {
			h.Set("Last-Modified", tc.lastModified)
		}
		if got := parseValidators(h); got != tc.want {
			t.Errorf("parseValidators(%q, %q) = %+v, want %+v", tc.etag, tc.lastModified, got, tc.want)
		}
	}
}

func TestAcceptRangesNone(t *testing.T) {
	tests := []struct {
		values []string
		want   bool
	}{
		{nil, false},
		{[]string{"bytes"}, false},
		{[]string{"none"}, true},
		{[]string{" None "}, true},
		{[]string{"none, bytes"}, false},
		{[]string{"none", "bytes"}, false},
		{[]string{"x-unit"}, false},
	}
	for _, tc := range tests {
		h := http.Header{"Accept-Ranges": tc.values}
		if got := acceptRangesNone(h); got != tc.want {
			t.Errorf("acceptRangesNone(%q) = %v, want %v", tc.values, got, tc.want)
		}
	}
}

func TestFetchRequestHeadersAndBody(t *testing.T) {
	var rec recorder
	ts := startServer(t, kindHTTP2TLS, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.WriteHeader(http.StatusOK)
	}))
	slot := newTestSlot(t, DefaultHTTPOptions(), ts)
	body := []byte("a=1&b=two")
	resp, err := slot.Fetch(testCtx(t), RangeRequest{
		Request: model.Request{
			URL:    ts.URL + "/post",
			Method: http.MethodPost,
			Body:   body,
			Headers: http.Header{
				"Cookie":            {"sid=123"},
				"Referer":           {"https://pages.example/list"},
				"User-Agent":        {"BrowserUA/1.0"},
				"Content-Type":      {"application/x-www-form-urlencoded"},
				"x-custom":          {"kept"},
				"Connection":        {"X-Hop"},
				"X-Hop":             {"dropped"},
				"If-None-Match":     {`"cached"`},
				"If-Modified-Since": {"Tue, 15 Nov 1994 08:12:31 GMT"},
				"Range":             {"bytes=5-6"},
				"Accept-Encoding":   {"gzip, br"},
				"Host":              {"evil.example"},
				"Te":                {"trailers"},
			},
		},
		Start: 0, End: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	readBody(t, resp)
	seen := rec.last(t)
	if seen.Method != http.MethodPost || !bytes.Equal(seen.Body, body) {
		t.Errorf("server saw %s with body %q", seen.Method, seen.Body)
	}
	want := map[string]string{
		"Cookie":          "sid=123",
		"Referer":         "https://pages.example/list",
		"User-Agent":      "BrowserUA/1.0",
		"Content-Type":    "application/x-www-form-urlencoded",
		"X-Custom":        "kept",
		"Range":           "bytes=0-",
		"Accept-Encoding": "identity",
	}
	for k, v := range want {
		if got := seen.Header.Get(k); got != v {
			t.Errorf("server saw %s %q, want %q", k, got, v)
		}
	}
	for _, k := range []string{"X-Hop", "If-None-Match", "If-Modified-Since"} {
		if v := seen.Header.Get(k); v != "" {
			t.Errorf("server saw %s %q, want it dropped", k, v)
		}
	}
	if strings.Contains(seen.Host, "evil") {
		t.Errorf("caller Host header reached the server: %q", seen.Host)
	}
}

func TestFetchInvalidRequests(t *testing.T) {
	ts := startServer(t, kindHTTP1, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("invalid request reached the server: %s %s", r.Method, r.URL)
	}))
	slot := newTestSlot(t, DefaultHTTPOptions(), ts)
	tests := []struct {
		name string
		rr   RangeRequest
	}{
		{"ftp scheme", RangeRequest{Request: model.Request{URL: "ftp://example.com/file"}, End: -1}},
		{"file scheme", RangeRequest{Request: model.Request{URL: "file:///etc/passwd"}, End: -1}},
		{"no host", RangeRequest{Request: model.Request{URL: "http:///path"}, End: -1}},
		{"unparsable url", RangeRequest{Request: model.Request{URL: "http://exa mple.com/%zz"}, End: -1}},
		{"negative start", RangeRequest{Request: model.Request{URL: ts.URL}, Start: -1, End: -1}},
		{"end before start", RangeRequest{Request: model.Request{URL: ts.URL}, Start: 10, End: 5}},
		{"end below -1", RangeRequest{Request: model.Request{URL: ts.URL}, Start: 0, End: -2}},
		{"bad method", RangeRequest{Request: model.Request{URL: ts.URL, Method: "GE T"}, End: -1}},
		{"header injection", RangeRequest{Request: model.Request{URL: ts.URL, Headers: http.Header{"Cookie": {"a=1\r\nX-Evil: 1"}}}, End: -1}},
		{"bad header name", RangeRequest{Request: model.Request{URL: ts.URL, Headers: http.Header{"Bad Name": {"x"}}}, End: -1}},
		{"garbage if-range", RangeRequest{Request: model.Request{URL: ts.URL}, End: -1, IfRange: "not-a-validator"}},
		{"whole from an offset", RangeRequest{Request: model.Request{URL: ts.URL}, Start: 5, End: -1, Whole: true}},
		{"whole and bounded", RangeRequest{Request: model.Request{URL: ts.URL}, End: 9, Whole: true}},
		{"whole with if-range", RangeRequest{Request: model.Request{URL: ts.URL}, End: -1, Whole: true, IfRange: `"tag"`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := slot.Fetch(testCtx(t), tc.rr)
			if err == nil {
				resp.Body.Close()
				t.Fatal("Fetch succeeded")
			}
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("err = %v, want ErrInvalidRequest", err)
			}
		})
	}
	//nolint:staticcheck // a nil context is exactly what is being tested
	if _, err := slot.Fetch(nil, RangeRequest{Request: model.Request{URL: ts.URL}, End: -1}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("nil context: err = %v", err)
	}
}

// A Whole request carries no Range header, even one the caller set, and still asks for
// identity; the response's Content-Encoding is reported as ContentCoding.
func TestFetchWholeAndContentCoding(t *testing.T) {
	for _, kind := range allKinds {
		t.Run(kind, func(t *testing.T) {
			var ranges, encodings []string
			ts := startServer(t, kind, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ranges = append(ranges, r.Header.Get("Range"))
				encodings = append(encodings, r.Header.Get("Accept-Encoding"))
				w.Header().Add("Content-Encoding", "GZIP, identity")
				w.Header().Add("Content-Encoding", "br")
				_, _ = w.Write([]byte("coded"))
			}))
			slot := newTestSlot(t, DefaultHTTPOptions(), ts)
			hdr := http.Header{"Range": {"bytes=1-2"}, "Accept-Encoding": {"gzip"}}
			for _, whole := range []bool{true, false} {
				resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: ts.URL, Headers: hdr}, End: -1, Whole: whole})
				if err != nil {
					t.Fatal(err)
				}
				readBody(t, resp)
				if !slices.Equal(resp.ContentCoding, []string{"gzip", "br"}) {
					t.Errorf("ContentCoding = %q, want [gzip br]", resp.ContentCoding)
				}
			}
			if !slices.Equal(ranges, []string{"", "bytes=0-"}) || !slices.Equal(encodings, []string{"identity", "identity"}) {
				t.Errorf("server saw Range %q, Accept-Encoding %q", ranges, encodings)
			}
		})
	}
}

func TestFetchRetryAfter(t *testing.T) {
	serverDate := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name       string
		status     int
		retryAfter string
		date       time.Time
		want       time.Duration
	}{
		{"429 seconds", http.StatusTooManyRequests, "120", time.Time{}, 120 * time.Second},
		{"503 http-date vs Date header", http.StatusServiceUnavailable, serverDate.Add(30 * time.Second).Format(http.TimeFormat), serverDate, 30 * time.Second},
		{"429 capped", http.StatusTooManyRequests, "99999999999", time.Time{}, MaxRetryAfter},
		{"503 malformed", http.StatusServiceUnavailable, "soon", time.Time{}, 0},
		{"503 date in the past", http.StatusServiceUnavailable, serverDate.Add(-time.Minute).Format(http.TimeFormat), serverDate, 0},
		{"200 ignored", http.StatusOK, "120", time.Time{}, 0},
		{"500 ignored", http.StatusInternalServerError, "120", time.Time{}, 0},
	}
	for _, kind := range []string{kindHTTP1, kindHTTP2TLS} {
		for _, tc := range tests {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				ts := startServer(t, kind, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Retry-After", tc.retryAfter)
					if !tc.date.IsZero() {
						w.Header().Set("Date", tc.date.Format(http.TimeFormat))
					}
					w.WriteHeader(tc.status)
				}))
				slot := newTestSlot(t, DefaultHTTPOptions(), ts)
				resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: ts.URL}, End: -1})
				if err != nil {
					t.Fatal(err)
				}
				readBody(t, resp)
				if resp.StatusCode != tc.status {
					t.Fatalf("status = %d", resp.StatusCode)
				}
				if resp.RetryAfter != tc.want {
					t.Fatalf("RetryAfter = %v, want %v", resp.RetryAfter, tc.want)
				}
			})
		}
	}
}

func TestFetchHeaderTooLarge(t *testing.T) {
	ts := startServer(t, kindHTTP1, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Big", strings.Repeat("a", 70<<10))
		w.WriteHeader(http.StatusOK)
	}))
	slot := newTestSlot(t, DefaultHTTPOptions(), ts)
	_, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: ts.URL}, End: -1})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}

	opts := DefaultHTTPOptions()
	opts.MaxHeaderBytes = 256 << 10
	big := newTestSlot(t, opts, ts)
	resp, err := big.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: ts.URL}, End: -1})
	if err != nil {
		t.Fatalf("with a raised MaxHeaderBytes: %v", err)
	}
	readBody(t, resp)
}

func TestFetchContextCancellation(t *testing.T) {
	for _, kind := range []string{kindHTTP1, kindHTTP2TLS} {
		t.Run(kind, func(t *testing.T) {
			release := make(chan struct{})
			ts := startServer(t, kind, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/body" {
					w.Header().Set("Content-Length", "1000000")
					_, _ = w.Write(make([]byte, 1000))
					w.(http.Flusher).Flush()
				}
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			defer close(release)
			slot := newTestSlot(t, DefaultHTTPOptions(), ts)

			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_, err := slot.Fetch(ctx, RangeRequest{Request: model.Request{URL: ts.URL + "/headers"}, End: -1})
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("waiting for headers: err = %v, want DeadlineExceeded", err)
			}

			ctx2, cancel2 := context.WithCancel(context.Background())
			resp, err := slot.Fetch(ctx2, RangeRequest{Request: model.Request{URL: ts.URL + "/body"}, End: -1})
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			buf := make([]byte, 1000)
			if _, err := io.ReadFull(resp.Body, buf); err != nil {
				t.Fatalf("first chunk: %v", err)
			}
			time.AfterFunc(50*time.Millisecond, cancel2)
			if _, err := io.ReadAll(resp.Body); err == nil {
				t.Fatal("body read succeeded after cancellation")
			}
		})
	}
}

// TestBodyCloseDrainsSmallRemainder checks docs/ENGINE-SPEC.md §5: an HTTP/1.1 body
// closed with at most 64 KiB left is drained so the slot reuses its connection, while a
// larger remainder closes it.
func TestBodyCloseDrainsSmallRemainder(t *testing.T) {
	const size = 400 << 10
	tests := []struct {
		name      string
		readFirst int
		wantConns int64
	}{
		{"small remainder is drained", size - 10<<10, 1},
		{"remainder at the limit is drained", size - maxDrainBytes, 1},
		{"large remainder closes", 1 << 10, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var conns atomic.Int64
			cs := newContentServer(size)
			ts := httptest.NewUnstartedServer(cs)
			ts.Config.ConnState = func(_ net.Conn, s http.ConnState) {
				if s == http.StateNew {
					conns.Add(1)
				}
			}
			ts.Start()
			t.Cleanup(ts.Close)
			slot := newTestSlot(t, DefaultHTTPOptions(), ts)

			resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: ts.URL}, End: -1})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.ReadFull(resp.Body, make([]byte, tc.readFirst)); err != nil {
				t.Fatal(err)
			}
			if err := resp.Body.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if err := resp.Body.Close(); err != nil {
				t.Fatalf("second Close: %v", err)
			}

			resp, err = slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: ts.URL}, Start: 10, End: 19})
			if err != nil {
				t.Fatal(err)
			}
			if got := readBody(t, resp); !bytes.Equal(got, cs.content[10:20]) {
				t.Fatal("second response body mismatch")
			}
			if got := conns.Load(); got != tc.wantConns {
				t.Fatalf("server saw %d connections, want %d", got, tc.wantConns)
			}
		})
	}
}

// TestHTTP2BodyCloseRetiresConnection checks docs/ENGINE-SPEC.md §5 for HTTP/2: a body
// closed with at most 64 KiB left only resets its stream and the slot reuses the
// connection, while a larger remainder also retires the connection, so that the slot's
// next response does not queue behind the reset stream's data already in flight.
func TestHTTP2BodyCloseRetiresConnection(t *testing.T) {
	const size = 400 << 10
	tests := []struct {
		name      string
		readFirst int
		wantConns int64
	}{
		{"small remainder reuses the connection", size - 10<<10, 1},
		{"remainder at the limit reuses the connection", size - maxDrainBytes, 1},
		{"read to the end reuses the connection", size, 1},
		{"large remainder retires the connection", 1 << 10, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var conns atomic.Int64
			cs := newContentServer(size)
			ts := httptest.NewUnstartedServer(cs)
			ts.Config.ErrorLog = log.New(io.Discard, "", 0)
			ts.Config.ConnState = func(_ net.Conn, s http.ConnState) {
				if s == http.StateNew {
					conns.Add(1)
				}
			}
			ts.EnableHTTP2 = true
			ts.StartTLS()
			t.Cleanup(ts.Close)
			slot := newTestSlot(t, DefaultHTTPOptions(), ts)

			resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: ts.URL}, End: -1})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.ReadFull(resp.Body, make([]byte, tc.readFirst)); err != nil {
				t.Fatal(err)
			}
			if err := resp.Body.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			resp, err = slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: ts.URL}, Start: 10, End: 19})
			if err != nil {
				t.Fatal(err)
			}
			if got := readBody(t, resp); !bytes.Equal(got, cs.content[10:20]) {
				t.Fatal("second response body mismatch")
			}
			for i, r := range cs.rec.all() {
				if r.Proto != 2 {
					t.Fatalf("request %d used HTTP/%d", i, r.Proto)
				}
			}
			if got := conns.Load(); got != tc.wantConns {
				t.Fatalf("server saw %d connections, want %d", got, tc.wantConns)
			}
		})
	}
}

// TestSlotsDoNotShareConnections checks that HTTP/2 does not coalesce worker slots onto
// one connection, while one slot reuses its own connection.
func TestSlotsDoNotShareConnections(t *testing.T) {
	cs := newContentServer(10_000)
	ts := startServer(t, kindHTTP2TLS, cs)
	f, err := newHTTPFactory(DefaultHTTPOptions(), trustPool(ts))
	if err != nil {
		t.Fatal(err)
	}
	slots := []Fetcher{f.NewSlot(), f.NewSlot(), f.NewSlot()}
	for _, s := range slots {
		defer s.Close()
	}
	for round := 0; round < 2; round++ {
		for i, s := range slots {
			resp, err := s.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: ts.URL}, Start: int64(i * 100), End: int64(i*100 + 99)})
			if err != nil {
				t.Fatal(err)
			}
			readBody(t, resp)
		}
	}
	reqs := cs.rec.all()
	if len(reqs) != 6 {
		t.Fatalf("server saw %d requests", len(reqs))
	}
	addrs := map[string]bool{}
	for i, r := range reqs {
		if r.Proto != 2 {
			t.Fatalf("request %d used HTTP/%d", i, r.Proto)
		}
		addrs[r.RemoteAddr] = true
		if i >= 3 && r.RemoteAddr != reqs[i-3].RemoteAddr {
			t.Errorf("slot %d did not reuse its connection: %s then %s", i-3, reqs[i-3].RemoteAddr, r.RemoteAddr)
		}
	}
	if len(addrs) != 3 {
		t.Fatalf("3 slots used %d connections, want 3", len(addrs))
	}
}

func TestFetcherCloseThenReuse(t *testing.T) {
	cs := newContentServer(100)
	ts := startServer(t, kindHTTP1, cs)
	slot := newTestSlot(t, DefaultHTTPOptions(), ts)
	for i := 0; i < 2; i++ {
		resp, err := slot.Fetch(testCtx(t), RangeRequest{Request: model.Request{URL: ts.URL}, End: -1})
		if err != nil {
			t.Fatalf("fetch %d: %v", i, err)
		}
		readBody(t, resp)
		slot.Close()
	}
}
