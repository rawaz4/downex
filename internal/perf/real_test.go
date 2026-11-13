//go:build perf

package perf

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// defaultRealURLs are public speed-test files of 100 MB (DOWNEX_PERF_REAL_URLS
// replaces them).
var defaultRealURLs = []string{
	"https://proof.ovh.net/files/100Mb.dat",
	"https://speedtest.tele2.net/100MB.zip",
	"https://fsn1-speed.hetzner.com/100MB.bin",
	"https://ash-speed.hetzner.com/100MB.bin",
	"http://cachefly.cachefly.net/100mb.test",
}

// rangeCheck is the answer of a host to "Range: bytes=0-0".
type rangeCheck struct {
	URL          string  `json:"url"`
	Status       int     `json:"status"`
	Ranged       bool    `json:"ranged"` // 206 with a Content-Range carrying the total
	ContentRange string  `json:"content_range,omitempty"`
	AcceptRanges string  `json:"accept_ranges,omitempty"`
	Size         int64   `json:"size"`
	Proto        string  `json:"proto,omitempty"`
	Server       string  `json:"server,omitempty"`
	RemoteAddr   string  `json:"remote_addr,omitempty"`
	TTFBms       float64 `json:"ttfb_ms"` // connect + TLS + first byte of a fresh connection
	Error        string  `json:"error,omitempty"`
}

func checkRange(ctx context.Context, rawURL string) rangeCheck {
	rc := rangeCheck{URL: rawURL, Size: -1}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	trace := &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) {
		if i.Conn != nil {
			rc.RemoteAddr = i.Conn.RemoteAddr().String()
		}
	}}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet, rawURL, nil)
	if err != nil {
		rc.Error = err.Error()
		return rc
	}
	req.Header.Set("Range", "bytes=0-0")
	req.Header.Set("Accept-Encoding", "identity")
	// Some speed-test servers (Hetzner's) drop Go's default User-Agent with an EOF; ask
	// as the engine does.
	req.Header.Set("User-Agent", "Downex/perf")
	tr := http.DefaultTransport.(*http.Transport).Clone()
	defer tr.CloseIdleConnections()
	t0 := time.Now()
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		rc.Error = err.Error()
		return rc
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	rc.TTFBms = float64(time.Since(t0).Microseconds()) / 1000
	rc.Status, rc.Proto = resp.StatusCode, resp.Proto
	rc.ContentRange, rc.AcceptRanges = resp.Header.Get("Content-Range"), resp.Header.Get("Accept-Ranges")
	rc.Server = resp.Header.Get("Server")
	if _, total, ok := strings.Cut(rc.ContentRange, "/"); ok {
		if n, err := strconv.ParseInt(total, 10, 64); err == nil {
			rc.Size = n
			rc.Ranged = resp.StatusCode == http.StatusPartialContent
		}
	}
	return rc
}

// curlSummary is curl's own account of a transfer (-w).
type curlSummary struct {
	HTTPCode      int     `json:"http_code"`
	SizeDownload  int64   `json:"size_download"`
	TimeTotal     float64 `json:"time_total"`
	TimeStart     float64 `json:"time_starttransfer"`
	TimeConnect   float64 `json:"time_connect"`
	TimeAppConn   float64 `json:"time_appconnect"`
	SpeedDownload float64 `json:"speed_download"`
	HTTPVersion   string  `json:"http_version"`
}

const curlFormat = "%{http_code} %{size_download} %{time_total} %{time_starttransfer} %{time_connect} %{time_appconnect} %{speed_download} %{http_version}"

// findCurl returns DOWNEX_PERF_CURL, else curl on PATH (Windows: the built-in curl.exe).
func findCurl() string {
	if p := os.Getenv("DOWNEX_PERF_CURL"); p != "" {
		return p
	}
	if p, err := exec.LookPath("curl"); err == nil {
		return p
	}
	if p := filepath.Join(os.Getenv("SystemRoot"), "System32", "curl.exe"); fileExists(p) {
		return p
	}
	return ""
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// runCurl downloads rawURL with one curl connection into work, measures and hashes it,
// and deletes the file.
func runCurl(ctx context.Context, curl, rawURL, work string, timeout time.Duration) (res runResult) {
	res.Tool, res.URL = "curl", rawURL
	f, err := os.CreateTemp(work, "curl-*.bin")
	if err != nil {
		res.Error = err.Error()
		return res
	}
	out := f.Name()
	f.Close()
	defer os.Remove(out)
	ctx, cancel := context.WithTimeout(ctx, timeout+10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, curl, "-s", "-S", "-L", "--max-time", strconv.Itoa(int(timeout.Seconds())),
		"-o", out, "-w", curlFormat, rawURL)
	t0 := time.Now()
	stdout, err := cmd.Output()
	res.WallS = time.Since(t0).Seconds()
	if cmd.ProcessState != nil {
		res.CPUS = (cmd.ProcessState.UserTime() + cmd.ProcessState.SystemTime()).Seconds()
		res.CPUCores = res.CPUS / max(res.WallS, 1e-9)
	}
	var cs curlSummary
	if _, perr := fmt.Sscanf(strings.TrimSpace(string(stdout)), "%d %d %g %g %g %g %g %s",
		&cs.HTTPCode, &cs.SizeDownload, &cs.TimeTotal, &cs.TimeStart, &cs.TimeConnect, &cs.TimeAppConn,
		&cs.SpeedDownload, &cs.HTTPVersion); perr == nil {
		res.Curl = &cs
		res.Bytes = cs.SizeDownload
		res.TTFBS = cs.TimeStart
	}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		res.Error = err.Error()
		return res
	}
	res.OK = res.Curl != nil && res.Curl.HTTPCode == http.StatusOK
	if !res.OK && res.Error == "" {
		res.Error = "curl: " + strings.TrimSpace(string(stdout))
	}
	res.AvgMiBps = float64(res.Bytes) / mib / max(res.WallS, 1e-9)
	res.ConnsMax = 1
	if res.OK {
		if sum, err := fileSHA256(out); err == nil {
			res.SHA256 = sum
		}
	}
	return res
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// realSuite measures the real hosts: a range check each, then per host downex runs at
// the default connections alternating with curl runs, then downex runs at 16
// connections, within the traffic budget.
func realSuite(t *testing.T, cfg config, rep *report) {
	ctx := context.Background()
	curl := findCurl()
	if curl == "" {
		t.Log("curl not found: no reference runs")
	}
	cl, err := newClient(ctx, true, nil)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer cl.close()
	var spent int64
	for _, raw := range cfg.realURLs {
		rc := checkRange(ctx, raw)
		rep.addRange(rc)
		t.Logf("range check %s: status %d %q ranged=%v ttfb=%.0f ms err=%q", raw, rc.Status, rc.ContentRange, rc.Ranged, rc.TTFBms, rc.Error)
		if rc.Error != "" || rc.Status >= 400 {
			continue
		}
		// The daemon resolves the proxy of a host once and caches it; resolve it before
		// measuring so that no run pays a WPAD lookup the daemon would not.
		if u, err := url.Parse(raw); err == nil {
			_, _ = cl.network.ResolveProxy(ctx, u)
		}
		size := max(rc.Size, 100_000_000)
		var plan []string
		for i := range max(cfg.realRuns, cfg.realCurl) {
			if i < cfg.realRuns {
				plan = append(plan, "downex")
			}
			if i < cfg.realCurl && curl != "" {
				plan = append(plan, "curl")
			}
		}
		for range cfg.realExtra {
			plan = append(plan, "downex-c16")
		}
		counts := map[string]int{}
		for _, tool := range plan {
			if spent+size > cfg.realBudget {
				t.Logf("traffic budget reached (%d of %d bytes): skipping the rest", spent, cfg.realBudget)
				rep.note(fmt.Sprintf("real: traffic budget of %d bytes reached; later runs skipped", cfg.realBudget))
				return
			}
			counts[tool]++
			var res runResult
			switch tool {
			case "curl":
				res = runCurl(ctx, curl, raw, cfg.work, cfg.realTimeout)
				res.Variant = "1 connection"
			default:
				conns := defaultMaxConnections
				if tool == "downex-c16" {
					conns = 16
				}
				res = runEngine(ctx, cfg.work, fileRun{url: raw, real: true, cl: cl, maxConn: conns, size: -1,
					timeout: cfg.realTimeout, hash: true})
				res.Tool, res.URL, res.Variant = "downex", raw, fmt.Sprintf("c%d", conns)
			}
			res.Scenario, res.Run = "R:"+hostOf(raw), counts[tool]
			spent += max(res.Bytes, 0)
			rep.addReal(res)
			_ = rep.write() // keep what is measured if the process is stopped
			t.Logf("%s %s %s run %d: ok=%v %.2f s %.2f MiB/s peak %.2f t90 %.2f conns %d err=%q",
				hostOf(raw), res.Tool, res.Variant, res.Run, res.OK, res.WallS, res.AvgMiBps, res.PeakMiBps, res.T90S, res.ConnsMax, res.Error)
		}
	}
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Host
	}
	return raw
}
