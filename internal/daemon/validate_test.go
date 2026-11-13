package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/testserver"
	"downex/internal/validate"
)

func TestAddValidation(t *testing.T) {
	ts := serve(t, testserver.Options{Size: 1024, Seed: 1})
	h := newHarness(t, harnessOptions{})
	file := filepath.Join(h.dir, "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	url := ts.URL + "/f"
	hdr := func(kv ...string) []*downexv1.Header {
		var out []*downexv1.Header
		for i := 0; i < len(kv); i += 2 {
			out = append(out, &downexv1.Header{Name: kv[i], Value: kv[i+1]})
		}
		return out
	}
	many := make([]*downexv1.Header, validate.MaxHeaderCount+1)
	for i := range many {
		many[i] = &downexv1.Header{Name: "X-N", Value: "v"}
	}

	cases := map[string]*downexv1.AddRequest{
		"missing url":           {},
		"bad scheme":            {Url: "ftp://example.com/file"},
		"file scheme":           {Url: "file:///etc/passwd"},
		"no host":               {Url: "http:///file"},
		"empty host":            {Url: "http://:80/file"},
		"relative url":          {Url: "example.com/file"},
		"malformed url":         {Url: "http://exa mple.com/%zz"},
		"control char in url":   {Url: "http://example.com/a\nb"},
		"url too long":          {Url: "http://example.com/" + strings.Repeat("a", validate.MaxURLBytes)},
		"method PUT":            {Url: url, Method: "PUT"},
		"method CONNECT":        {Url: url, Method: "CONNECT"},
		"body with GET":         {Url: url, Body: []byte("x")},
		"huge body":             {Url: url, Method: "POST", Body: make([]byte, validate.MaxBodyBytes+1)},
		"CRLF header":           {Url: url, Headers: hdr("Referer", "https://a.example/\r\nX-Injected: 1")},
		"LF header":             {Url: url, Headers: hdr("Cookie", "a=b\nc")},
		"NUL header":            {Url: url, Headers: hdr("Cookie", "a\x00b")},
		"bad header name":       {Url: url, Headers: hdr("Bad Name", "v")},
		"empty header name":     {Url: url, Headers: hdr("", "v")},
		"header name with CRLF": {Url: url, Headers: hdr("X-A\r\nX-B", "v")},
		"Host header":           {Url: url, Headers: hdr("Host", "evil.example")},
		"Content-Length":        {Url: url, Headers: hdr("content-length", "5")},
		"Transfer-Encoding":     {Url: url, Headers: hdr("Transfer-Encoding", "chunked")},
		"Connection":            {Url: url, Headers: hdr("Connection", "close")},
		"Keep-Alive":            {Url: url, Headers: hdr("Keep-Alive", "300")},
		"Upgrade":               {Url: url, Headers: hdr("Upgrade", "h2c")},
		"TE":                    {Url: url, Headers: hdr("TE", "trailers")},
		"Range":                 {Url: url, Headers: hdr("range", "bytes=0-")},
		"If-Range":              {Url: url, Headers: hdr("If-Range", `"x"`)},
		"Proxy-Authorization":   {Url: url, Headers: hdr("Proxy-Authorization", "Basic eA==")},
		"too many headers":      {Url: url, Headers: many},
		"headers too large":     {Url: url, Headers: hdr("Cookie", strings.Repeat("c", validate.MaxHeaderBytes))},
		"relative dir":          {Url: url, Dir: "downloads"},
		"dot dir":               {Url: url, Dir: "."},
		"dir without parent":    {Url: url, Dir: filepath.Join(h.dir, "no", "such", "dir")},
		"dir is a file":         {Url: url, Dir: file},
		"filename with slash":   {Url: url, Filename: "../evil.txt"},
		"filename with subdir":  {Url: url, Filename: "sub/file.txt"},
		"filename backslash":    {Url: url, Filename: `..\..\evil.exe`},
		"filename dotdot":       {Url: url, Filename: ".."},
		"filename too long":     {Url: url, Filename: strings.Repeat("n", validate.MaxFilenameBytes+1)},
		"too many connections":  {Url: url, MaxConnections: MaxConnectionsLimit + 1},
		"negative connections":  {Url: url, MaxConnections: -1},
		"negative speed limit":  {Url: url, SpeedLimit: -1},
	}
	ctx := ctxT(t)
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := h.dl.Add(ctx, connect.NewRequest(req))
			if connectCode(err) != connect.CodeInvalidArgument {
				t.Fatalf("Add = %v, want InvalidArgument", err)
			}
			// Messages never echo header values.
			if msg := err.Error(); strings.Contains(msg, "X-Injected") || strings.Contains(msg, "Basic eA==") {
				t.Fatalf("message leaks a header value: %s", msg)
			}
		})
	}
	if n := len(h.list()); n != 0 {
		t.Fatalf("rejected requests created %d downloads", n)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "no")); err == nil {
		t.Fatal("a rejected request created directories")
	}

	// Accepted edge cases: sanitised names, POST bodies, browser headers the engine
	// replaces, and a missing directory below an existing one.
	newDir := filepath.Join(h.dir, "new")
	d := h.add(&downexv1.AddRequest{
		Url: url, Method: "post", Body: []byte("q=1"), Dir: newDir + string(filepath.Separator) + ".",
		Filename: "a:b?<c>.txt", StartPaused: true,
		Headers: hdr("Cookie", "s=1", "Accept-Encoding", "gzip, br", "If-None-Match", `"x"`, "User-Agent", "UA\tTab"),
	})
	if d.GetFilename() != "a_b__c_.txt" || d.GetDir() != newDir {
		t.Fatalf("added %q in %q", d.GetFilename(), d.GetDir())
	}
	rec := storedState(t, h.st, d.GetId())
	if rec.Request.Method != "POST" || string(rec.Request.Body) != "q=1" || rec.Request.Headers.Get("Cookie") != "s=1" {
		t.Fatalf("stored request = %+v", rec.Request)
	}
	if fi, err := os.Stat(newDir); err != nil || !fi.IsDir() {
		t.Fatalf("dir not created: %v", err)
	}
}

func TestResumeAndSettingsValidation(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	ctx := ctxT(t)
	d := h.add(&downexv1.AddRequest{Url: "http://127.0.0.1:9/x", StartPaused: true})

	tooMany, negative := int32(MaxConnectionsLimit+1), int64(-5)
	for name, req := range map[string]*downexv1.ResumeRequest{
		"max_connections": {Id: d.GetId(), MaxConnections: &tooMany},
		"speed_limit":     {Id: d.GetId(), SpeedLimit: &negative},
	} {
		if _, err := h.dl.Resume(ctx, connect.NewRequest(req)); connectCode(err) != connect.CodeInvalidArgument {
			t.Errorf("resume with bad %s: %v", name, err)
		}
	}
	for name, s := range map[string]*downexv1.Settings{
		"concurrency 17":     {MaxConcurrentDownloads: MaxConcurrentDownloadsLimit + 1},
		"concurrency -1":     {MaxConcurrentDownloads: -1},
		"negative limit":     {GlobalSpeedLimit: -1},
		"connections 33":     {DefaultMaxConnections: MaxConnectionsLimit + 1},
		"connections -2":     {DefaultMaxConnections: -2},
		"relative dir":       {DefaultDir: "relative/dir"},
		"default dir a file": {DefaultDir: os.Args[0]},
	} {
		if _, err := h.dm.UpdateSettings(ctx, connect.NewRequest(&downexv1.UpdateSettingsRequest{Settings: s})); connectCode(err) != connect.CodeInvalidArgument {
			t.Errorf("settings with %s: %v", name, err)
		}
	}
	if _, err := h.dm.UpdateSettings(ctx, connect.NewRequest(&downexv1.UpdateSettingsRequest{})); connectCode(err) != connect.CodeInvalidArgument {
		t.Errorf("settings missing: %v", err)
	}
	if s := h.m.Settings(); !s.Equal(defaultsFor(h.dir)) {
		t.Fatalf("rejected updates changed the settings: %+v", s)
	}
}
