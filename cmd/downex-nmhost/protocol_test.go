package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/validate"
)

// TestValidation checks every request field against the daemon's limits
// (docs/DAEMON-SPEC.md §4): rejected requests are answered with invalid_request and
// the request's id, and messages never repeat header values.
func TestValidation(t *testing.T) {
	const u = "https://example.test/file.bin"
	add := func(kv ...any) map[string]any {
		m := map[string]any{"id": "x", "type": "add", "url": u}
		for i := 0; i < len(kv); i += 2 {
			if kv[i+1] == nil {
				delete(m, kv[i].(string))
			} else {
				m[kv[i].(string)] = kv[i+1]
			}
		}
		return m
	}
	hello := func(kv ...any) map[string]any {
		m := map[string]any{"id": "x", "type": "hello", "protocol": 1, "browser": "chrome", "extVersion": "1.0"}
		for i := 0; i < len(kv); i += 2 {
			if kv[i+1] == nil {
				delete(m, kv[i].(string))
			} else {
				m[kv[i].(string)] = kv[i+1]
			}
		}
		return m
	}
	hdrs := func(kv ...string) [][]string {
		var out [][]string
		for i := 0; i < len(kv); i += 2 {
			out = append(out, []string{kv[i], kv[i+1]})
		}
		return out
	}
	many := make([][]string, validate.MaxHeaderCount+1)
	for i := range many {
		many[i] = []string{"X-N", "v"}
	}
	maxBody := base64.StdEncoding.EncodeToString(make([]byte, validate.MaxBodyBytes))
	bigBody := base64.StdEncoding.EncodeToString(make([]byte, validate.MaxBodyBytes+1))

	bad := map[string]map[string]any{
		"type not a string":     {"id": "x", "type": 5},
		"unknown type":          {"id": "x", "type": "remove"},
		"missing url":           add("url", nil),
		"url not a string":      add("url", 42),
		"empty url":             add("url", ""),
		"ftp url":               add("url", "ftp://example.test/f"),
		"javascript url":        add("url", "javascript:alert(1)"),
		"blob url":              add("url", "blob:https://example.test/uuid"),
		"no host":               add("url", "http:///f"),
		"relative url":          add("url", "example.test/f"),
		"url too long":          add("url", u+"?"+strings.Repeat("a", validate.MaxURLBytes)),
		"control char url":      add("url", "http://example.test/a\nb"),
		"method PUT":            add("method", "PUT"),
		"method not a string":   add("method", true),
		"headers not an array":  add("headers", "Cookie: a"),
		"header not a pair":     add("headers", [][]string{{"Cookie"}}),
		"header triple":         add("headers", [][]string{{"Cookie", "a", "b"}}),
		"header null":           add("headers", []any{nil}),
		"header number":         add("headers", []any{[]any{"X", 1}}),
		"CRLF header":           add("headers", hdrs("Referer", "https://a.example/\r\nX-Injected: 1")),
		"NUL header":            add("headers", hdrs("Cookie", "a\x00b")),
		"bad header name":       add("headers", hdrs("Bad Name", "v")),
		"empty header name":     add("headers", hdrs("", "v")),
		"Host header":           add("headers", hdrs("Host", "evil.example")),
		"Content-Length":        add("headers", hdrs("content-length", "5")),
		"Transfer-Encoding":     add("headers", hdrs("Transfer-Encoding", "chunked")),
		"Range header":          add("headers", hdrs("Range", "bytes=0-")),
		"Proxy-Authorization":   add("headers", hdrs("Proxy-Authorization", "Basic eA==")),
		"too many headers":      add("headers", many),
		"headers too large":     add("headers", hdrs("Cookie", strings.Repeat("c", validate.MaxHeaderBytes))),
		"body with GET":         add("body", "eA=="),
		"body not base64":       add("method", "POST", "body", "!!not base64!!"),
		"body too large":        add("method", "POST", "body", bigBody),
		"body not a string":     add("method", "POST", "body", 12),
		"filename with slash":   add("filename", "../evil.txt"),
		"filename backslash":    add("filename", `..\evil.exe`),
		"filename dotdot":       add("filename", ".."),
		"filename too long":     add("filename", strings.Repeat("n", validate.MaxFilenameBytes+1)),
		"referrer file":         add("referrer", "file:///etc/passwd"),
		"referrer CRLF":         add("referrer", "https://a.example/\r\nX: 1"),
		"mime control":          add("mime", "text/html\r\n"),
		"mime too long":         add("mime", strings.Repeat("m", validate.MaxMIMEBytes+1)),
		"size negative":         add("size", -2),
		"size fractional":       add("size", 1.5),
		"size string":           add("size", "12"),
		"startPaused string":    add("startPaused", "yes"),
		"deadline zero":         add("deadlineMs", 0),
		"deadline negative":     add("deadlineMs", -1500),
		"deadline fractional":   add("deadlineMs", 1500.5),
		"deadline string":       add("deadlineMs", "1500"),
		"hello without proto":   hello("protocol", nil),
		"hello protocol 2":      hello("protocol", 2),
		"hello protocol string": hello("protocol", "1"),
		"hello unknown browser": hello("browser", "netscape"),
		"hello no browser":      hello("browser", nil),
		"hello long version":    hello("extVersion", strings.Repeat("9", maxExtVersionBytes+1)),
		"hello control version": hello("extVersion", "1\n2"),
	}
	for name, m := range bad {
		t.Run(name, func(t *testing.T) {
			b, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			req, rerr := parseRequest(b)
			if rerr == nil {
				t.Fatalf("accepted: %+v", req)
			}
			if rerr.id != "x" {
				t.Fatalf("error without the request id: %+v", rerr)
			}
			for _, leak := range []string{"X-Injected", "Basic eA==", "a\x00b"} {
				if strings.Contains(rerr.msg, leak) {
					t.Fatalf("message leaks a header value: %s", rerr.msg)
				}
			}
		})
	}

	good := map[string]map[string]any{
		"minimal add":         add(),
		"post with max body":  add("method", "POST", "body", maxBody),
		"lower-case method":   add("method", "post", "body", "eA=="),
		"empty body string":   add("body", ""),
		"browser headers":     add("headers", hdrs("Cookie", "a=b", "User-Agent", "UA\tTab", "Accept-Encoding", "gzip", "If-None-Match", `"x"`)),
		"sanitised filename":  add("filename", "a:b?<c>.txt"),
		"unknown size":        add("size", -1),
		"unknown fields":      add("future", map[string]any{"x": 1}),
		"deadline":            add("deadlineMs", 1500),
		"huge deadline":       add("deadlineMs", int64(1)<<62),
		"null optional":       add("filename", nil, "referrer", "", "pageUrl", ""),
		"page":                add("pageUrl", "https://pages.example/download?id=1"),
		"hello other":         hello("browser", "other"),
		"hello no extVersion": hello("extVersion", nil),
		"status":              {"id": "x", "type": "status"},
		"ping":                {"id": "x", "type": "ping"},
	}
	for name, m := range good {
		t.Run(name, func(t *testing.T) {
			b, _ := json.Marshal(m)
			if _, rerr := parseRequest(b); rerr != nil {
				t.Fatalf("rejected: %v", rerr)
			}
		})
	}
}

// TestAddDeadline checks that deadlineMs is kept, capped at the relay's own add timeout.
func TestAddDeadline(t *testing.T) {
	for in, want := range map[string]time.Duration{
		`{"id":"x","type":"add","url":"https://example.test/f","deadlineMs":1500}`:                1500 * time.Millisecond,
		`{"id":"x","type":"add","url":"https://example.test/f","deadlineMs":4611686018427387904}`: addTimeout,
		`{"id":"x","type":"add","url":"https://example.test/f"}`:                                  0,
	} {
		req, rerr := parseRequest([]byte(in))
		if rerr != nil {
			t.Fatalf("%s: %v", in, rerr)
		}
		if req.info.deadline != want {
			t.Errorf("%s: deadline %v, want %v", in, req.info.deadline, want)
		}
	}
}

func TestResponsesStayWithinLimits(t *testing.T) {
	r := failure("id", codeRejected, strings.Repeat("é", 3*maxMessageBytes)+"\xff")
	b := r.encode()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	msg := m["error"].(map[string]any)["message"].(string)
	if len(msg) > maxMessageBytes+3 || !strings.HasSuffix(msg, "...") {
		t.Fatalf("message of %d bytes", len(msg))
	}
	// A response over the browser's limit becomes an internal error.
	huge := success("big", "status", map[string]any{"pad": strings.Repeat("p", maxResponseBytes)})
	if err := json.Unmarshal(huge.encode(), &m); err != nil || m["ok"] != false || m["id"] != "big" {
		t.Fatalf("oversized response: %v %v", m, err)
	}
}

func TestRedactURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://u:p@example.test/a/b.zip?sig=1#frag": "https://[redacted]@example.test/a/b.zip?[redacted]",
		"http://example.test":                         "http://example.test",
		"%zz":                                         "(unparsable URL)",
		"":                                            "",
	} {
		if got := redactURL(in); got != want {
			t.Errorf("redactURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// The relay marks adds as browser hand-offs and forwards the size and MIME hints.
func TestAddHints(t *testing.T) {
	for _, c := range []struct {
		msg      string
		wantSize int64
		wantMIME string
	}{
		{`{"id":"1","type":"add","url":"https://x.test/a.mp4","size":42,"mime":"Video/MP4; codecs=avc1"}`, 42, "video/mp4"},
		{`{"id":"1","type":"add","url":"https://x.test/a","size":-1,"mime":"not a mime"}`, 0, ""},
		{`{"id":"1","type":"add","url":"https://x.test/a","mime":"vidéo/mp4"}`, 0, ""},
		{`{"id":"1","type":"add","url":"https://x.test/a"}`, 0, ""},
	} {
		r, rerr := parseRequest([]byte(c.msg))
		if rerr != nil {
			t.Fatalf("%s: %v", c.msg, rerr)
		}
		if r.add.GetSource() != downexv1.AddSource_ADD_SOURCE_BROWSER || r.add.GetExpectedSize() != c.wantSize ||
			r.add.GetMime() != c.wantMIME {
			t.Errorf("%s: forwarded %v", c.msg, r.add)
		}
	}
}
