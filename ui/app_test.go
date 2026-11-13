package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	downexv1 "downex/internal/rpc/downex/v1"
)

// frontendError is how frontend/src/lib/api.ts (toApiError) splits a rejection.
var frontendError = regexp.MustCompile(`(?s)^([a-z_]+):\s*(.*)$`)

func TestBindingsAreExactlyTheContract(t *testing.T) {
	raw := reflect.TypeFor[json.RawMessage]()
	want := map[string]struct {
		in  []reflect.Type
		out []reflect.Type
	}{
		"List":           {nil, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"Get":            {[]reflect.Type{raw}, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"Add":            {[]reflect.Type{raw}, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"Pause":          {[]reflect.Type{raw}, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"Resume":         {[]reflect.Type{raw}, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"Remove":         {[]reflect.Type{raw}, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"Confirm":        {[]reflect.Type{raw}, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"AddMedia":       {[]reflect.Type{raw}, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"ProbeMedia":     {[]reflect.Type{raw}, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"PauseAll":       {nil, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"ResumeAll":      {nil, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"GetSettings":    {nil, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"UpdateSettings": {[]reflect.Type{raw}, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"Status":         {nil, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"OpenFile":       {[]reflect.Type{raw}, []reflect.Type{reflect.TypeFor[error]()}},
		"OpenFolder":     {[]reflect.Type{raw}, []reflect.Type{reflect.TypeFor[error]()}},
		"PickFolder":     {[]reflect.Type{raw}, []reflect.Type{reflect.TypeFor[string](), reflect.TypeFor[error]()}},
		// The download complete dialog (docs/DESKTOP-SPEC.md §8).
		"OpenWith": {[]reflect.Type{raw}, []reflect.Type{reflect.TypeFor[error]()}},
		// The progress window (docs/DESKTOP-SPEC.md §7).
		"SetDownloadOptions": {[]reflect.Type{raw}, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"OpenProgressWindow": {[]reflect.Type{raw}, []reflect.Type{reflect.TypeFor[error]()}},
		"Startup":            {nil, []reflect.Type{raw, reflect.TypeFor[error]()}},
		// Queues, secrets, updates and the post-queue action (docs/M6-SPEC.md).
		"ListQueues":       {nil, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"CreateQueue":      {[]reflect.Type{raw}, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"UpdateQueue":      {[]reflect.Type{raw}, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"DeleteQueue":      {[]reflect.Type{raw}, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"StartQueue":       {[]reflect.Type{raw}, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"StopQueue":        {[]reflect.Type{raw}, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"MoveDownloads":    {[]reflect.Type{raw}, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"CancelPostAction": {[]reflect.Type{raw}, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"SetSecret":        {[]reflect.Type{raw}, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"ClearSecret":      {[]reflect.Type{raw}, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"CheckForUpdate":   {nil, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"InstallUpdate":    {nil, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"PickFile":         {[]reflect.Type{raw}, []reflect.Type{reflect.TypeFor[string](), reflect.TypeFor[error]()}},
		// yt-dlp and Deno for video pages (docs/SITE-MEDIA-SPEC.md §5.4).
		"GetTools":     {nil, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"InstallTools": {[]reflect.Type{raw}, []reflect.Type{raw, reflect.TypeFor[error]()}},
		// The free space of the download folder's volume (docs/DESIGN-SYSTEM.md §6.2).
		"FreeSpace": {[]reflect.Type{raw}, []reflect.Type{raw, reflect.TypeFor[error]()}},
		// The app's theme for the native window parts, and Settings → About.
		"SetTheme":          {[]reflect.Type{raw}, []reflect.Type{reflect.TypeFor[error]()}},
		"OpenAppFolder":     {[]reflect.Type{raw}, []reflect.Type{reflect.TypeFor[error]()}},
		"Diagnostics":       {[]reflect.Type{raw}, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"ThirdPartyNotices": {nil, []reflect.Type{raw, reflect.TypeFor[error]()}},
		"FileExists":        {[]reflect.Type{raw}, []reflect.Type{raw, reflect.TypeFor[error]()}},
	}
	typ := reflect.TypeFor[*App]()
	var got []string
	for i := range typ.NumMethod() {
		m := typ.Method(i)
		got = append(got, m.Name)
		w, ok := want[m.Name]
		if !ok {
			t.Errorf("method %s would be bound to the frontend but is not in the contract", m.Name)
			continue
		}
		var in, out []reflect.Type
		for j := 1; j < m.Type.NumIn(); j++ { // skip the receiver
			in = append(in, m.Type.In(j))
		}
		for j := range m.Type.NumOut() {
			out = append(out, m.Type.Out(j))
		}
		if !slices.Equal(in, w.in) || !slices.Equal(out, w.out) {
			t.Errorf("%s has signature %v, want (%v) (%v)", m.Name, m.Type, w.in, w.out)
		}
	}
	if len(got) != len(want) {
		t.Errorf("bound methods %v, want the %d of the contract", got, len(want))
	}
}

func ts(s string) *timestamppb.Timestamp {
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return timestamppb.New(tm)
}

func sampleDownload() *downexv1.Download {
	return &downexv1.Download{
		Id: 7, Url: "https://example.com/a.iso", FinalUrl: "https://cdn.example.com/a.iso",
		Dir: `C:\Downloads`, Filename: "a.iso", Size: -1, Received: 5 << 40,
		State: downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING, Mode: downexv1.Mode_MODE_RANGED,
		MaxConnections: 8, SpeedLimit: 3 << 32,
		CreatedAt: ts("2026-10-20T09:00:00Z"), UpdatedAt: ts("2026-10-20T09:00:01.5Z"),
		SpeedBps: 1234.5, Connections: 4,
		Segments: []*downexv1.Segment{{Start: 0, End: 99, Received: 10}, {Start: 100, End: -1, Received: 0}},
	}
}

func TestListJSONShape(t *testing.T) {
	completed := &downexv1.Download{
		Id: 8, Dir: `C:\Downloads`, Filename: "b.zip", Size: 0, State: downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED,
		Mode: downexv1.Mode_MODE_SINGLE, CompletedAt: ts("2026-10-20T10:00:00Z"), Error: "", ErrorCode: "",
	}
	a, _ := newTestApp(t, newFakeDaemon(sampleDownload(), completed), nil)
	raw, err := a.List()
	if err != nil {
		t.Fatal(err)
	}
	v := decodeJSON(t, raw)
	list, ok := v["downloads"].([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("downloads = %#v", v["downloads"])
	}
	d := list[0].(map[string]any)
	want := map[string]any{
		"id":             "7", // int64 as a decimal string
		"url":            "https://example.com/a.iso",
		"finalUrl":       "https://cdn.example.com/a.iso", // lowerCamelCase, not final_url
		"dir":            `C:\Downloads`,
		"filename":       "a.iso",
		"size":           "-1", // unknown size is sent explicitly
		"received":       "5497558138880",
		"state":          "DOWNLOAD_STATE_DOWNLOADING",
		"mode":           "MODE_RANGED",
		"maxConnections": float64(8),
		"speedLimit":     "12884901888",
		"createdAt":      "2026-10-20T09:00:00Z",
		"updatedAt":      "2026-10-20T09:00:01.500Z",
		"speedBps":       1234.5,
		"connections":    float64(4),
		"segments": []any{
			map[string]any{"end": "99", "received": "10"}, // start 0 is the default
			map[string]any{"start": "100", "end": "-1"},
		},
	}
	if !reflect.DeepEqual(d, want) {
		t.Errorf("download 7 =\n%#v\nwant\n%#v", d, want)
	}
	// Defaults are omitted (the frontend reads them as 0, "", unset), but never size -1.
	d = list[1].(map[string]any)
	for _, k := range []string{"size", "received", "error", "errorCode", "createdAt", "queuePosition", "segments", "final_url", "completed_at"} {
		if _, ok := d[k]; ok {
			t.Errorf("download 8 has %q: %v", k, d[k])
		}
	}
	if d["completedAt"] != "2026-10-20T10:00:00Z" || d["state"] != "DOWNLOAD_STATE_COMPLETED" || d["mode"] != "MODE_SINGLE" {
		t.Errorf("download 8 = %v", d)
	}
}

func TestAddForwardsTheRequest(t *testing.T) {
	f := newFakeDaemon()
	a, _ := newTestApp(t, f, nil)
	raw, err := a.Add(json.RawMessage(`{"url":"https://example.com/f?x=1","method":"POST",
		"headers":[{"name":"Referer","value":"https://example.com/"}],"dir":"C:\\Data","filename":"f.bin",
		"maxConnections":4,"speedLimit":"1048576","startPaused":true}`))
	if err != nil {
		t.Fatal(err)
	}
	want := &downexv1.AddRequest{
		Url: "https://example.com/f?x=1", Method: "POST",
		Headers: []*downexv1.Header{{Name: "Referer", Value: "https://example.com/"}},
		Dir:     `C:\Data`, Filename: "f.bin", MaxConnections: 4, SpeedLimit: 1 << 20, StartPaused: true,
	}
	if got := f.lastRequest(t); !proto.Equal(got, want) {
		t.Errorf("daemon got %v, want %v", got, want)
	}
	d := decodeJSON(t, raw)["download"].(map[string]any)
	if d["id"] != "99" || d["state"] != "DOWNLOAD_STATE_QUEUED" || d["size"] != "-1" || d["queuePosition"] != float64(1) ||
		d["speedLimit"] != "1048576" {
		t.Errorf("AddResponse.download = %v", d)
	}
}

func TestPauseResumeRemove(t *testing.T) {
	f := newFakeDaemon(sampleDownload())
	a, _ := newTestApp(t, f, nil)

	raw, err := a.Pause(json.RawMessage(`{"id":7}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.lastRequest(t); !proto.Equal(got, &downexv1.PauseRequest{Id: 7}) {
		t.Errorf("Pause sent %v", got)
	}
	if d := decodeJSON(t, raw)["download"].(map[string]any); d["state"] != "DOWNLOAD_STATE_PAUSED" || d["id"] != "7" {
		t.Errorf("PauseResponse = %s", raw)
	}

	// Optional fields keep their presence: absent stays absent, 0 is sent as 0.
	if _, err := a.Resume(json.RawMessage(`{"id":7,"restart":false}`)); err != nil {
		t.Fatal(err)
	}
	if got := f.lastRequest(t).(*downexv1.ResumeRequest); got.MaxConnections != nil || got.SpeedLimit != nil || got.GetRestart() {
		t.Errorf("Resume without options sent %v", got)
	}
	if _, err := a.Resume(json.RawMessage(`{"id":"7","restart":true,"maxConnections":3,"speedLimit":0}`)); err != nil {
		t.Fatal(err)
	}
	got := f.lastRequest(t).(*downexv1.ResumeRequest)
	if got.GetId() != 7 || !got.GetRestart() || got.MaxConnections == nil || got.GetMaxConnections() != 3 ||
		got.SpeedLimit == nil || got.GetSpeedLimit() != 0 {
		t.Errorf("Resume with options sent %v", got)
	}

	raw, err = a.Remove(json.RawMessage(`{"id":7,"deleteFile":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "{}" {
		t.Errorf("Remove returned %s, want {}", raw)
	}
	if got := f.lastRequest(t); !proto.Equal(got, &downexv1.RemoveRequest{Id: 7, DeleteFile: true}) {
		t.Errorf("Remove sent %v", got)
	}
}

func TestSettingsAndStatus(t *testing.T) {
	f := newFakeDaemon()
	f.settings = &downexv1.Settings{MaxConcurrentDownloads: 3, GlobalSpeedLimit: 0, DefaultDir: `C:\Users\me\Downloads`, DefaultMaxConnections: 8}
	f.status = &downexv1.StatusResponse{Version: "1.2.3", Pid: 4242, StartedAt: ts("2026-10-20T08:00:00Z"), Active: 1, Queued: 2, Endpoint: `\\.\pipe\downex-x`}
	a, _ := newTestApp(t, f, nil)

	raw, err := a.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"settings": map[string]any{
		"maxConcurrentDownloads": float64(3), "defaultDir": `C:\Users\me\Downloads`, "defaultMaxConnections": float64(8),
	}}
	if got := decodeJSON(t, raw); !reflect.DeepEqual(got, want) {
		t.Errorf("GetSettings = %v, want %v", got, want)
	}

	// The frontend sends globalSpeedLimit as a number.
	raw, err = a.UpdateSettings(json.RawMessage(`{"settings":{"maxConcurrentDownloads":4,"globalSpeedLimit":10485760,
		"defaultDir":"D:\\dl","defaultMaxConnections":16}}`))
	if err != nil {
		t.Fatal(err)
	}
	wantReq := &downexv1.UpdateSettingsRequest{Settings: &downexv1.Settings{
		MaxConcurrentDownloads: 4, GlobalSpeedLimit: 10 << 20, DefaultDir: `D:\dl`, DefaultMaxConnections: 16,
	}}
	if got := f.lastRequest(t); !proto.Equal(got, wantReq) {
		t.Errorf("UpdateSettings sent %v", got)
	}
	if s := decodeJSON(t, raw)["settings"].(map[string]any); s["globalSpeedLimit"] != "10485760" || s["defaultDir"] != `D:\dl` {
		t.Errorf("UpdateSettingsResponse = %s", raw)
	}

	raw, err = a.Status()
	if err != nil {
		t.Fatal(err)
	}
	wantStatus := map[string]any{"version": "1.2.3", "pid": float64(4242), "startedAt": "2026-10-20T08:00:00Z",
		"active": float64(1), "queued": float64(2), "endpoint": `\\.\pipe\downex-x`}
	if got := decodeJSON(t, raw); !reflect.DeepEqual(got, wantStatus) {
		t.Errorf("Status = %v, want %v", got, wantStatus)
	}
}

func TestErrorsAreConnectText(t *testing.T) {
	f := newFakeDaemon(sampleDownload())
	a, _ := newTestApp(t, f, nil)
	check := func(name string, err error, want string) {
		t.Helper()
		if err == nil {
			t.Errorf("%s: no error, want %q", name, want)
			return
		}
		if !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%s: %q, want prefix %q", name, err.Error(), want)
		}
		if m := frontendError.FindStringSubmatch(err.Error()); m == nil || m[2] == "" {
			t.Errorf("%s: %q is not \"<code>: <message>\"", name, err.Error())
		}
	}

	_, err := a.Pause(json.RawMessage(`{"id":3}`))
	check("unknown id", err, "not_found: no such download")

	_, err = a.Pause(json.RawMessage(`{"id":`))
	check("broken JSON", err, "invalid_argument: malformed request: ")
	_, err = a.Resume(json.RawMessage(`{"id":7,"bogus":1}`))
	check("unknown field", err, "invalid_argument: malformed request: ")
	_, err = a.Add(json.RawMessage(`{"url":"https://e.example/","maxConnections":"many"}`))
	check("wrong type", err, "invalid_argument: malformed request: ")
	_, err = a.PickFolder(json.RawMessage(`{"initialDir":1}`))
	check("PickFolder argument", err, "invalid_argument: malformed request: ")
	err = a.OpenFile(json.RawMessage(`[]`))
	check("OpenFile argument", err, "invalid_argument: malformed request: ")

	for _, tc := range []struct {
		err  error
		want string
	}{
		{connect.NewError(connect.CodeFailedPrecondition, errors.New("download 7 is completed")), "failed_precondition: download 7 is completed"},
		{connect.NewError(connect.CodeUnavailable, fmt.Errorf("connect to downexd: %w", errors.New("pipe busy"))), "unavailable: connect to downexd: pipe busy"},
		{fmt.Errorf("wrapped: %w", connect.NewError(connect.CodeInvalidArgument, errors.New("bad url"))), "invalid_argument: wrapped: invalid_argument: bad url"},
		{context.DeadlineExceeded, "deadline_exceeded: downexd did not answer in time"},
		{errors.New("something else"), "unknown: something else"},
		{connect.NewError(connect.CodeInternal, nil), "internal: no details"},
	} {
		f.mu.Lock()
		f.err = tc.err
		f.mu.Unlock()
		_, err := a.List()
		check(tc.want, err, tc.want)
		if err != nil && err.Error() != tc.want {
			t.Errorf("error %v is reported as %q, want %q", tc.err, err.Error(), tc.want)
		}
	}
}

func TestAbsentArgumentIsEmptyRequest(t *testing.T) {
	f := newFakeDaemon()
	a, _ := newTestApp(t, f, nil)
	_, err := a.Pause(json.RawMessage(`null`))
	if err == nil || !strings.HasPrefix(err.Error(), "not_found:") {
		t.Fatalf("Pause(null) = %v, want the daemon's answer to id 0", err)
	}
	if got := f.lastRequest(t); !proto.Equal(got, &downexv1.PauseRequest{}) {
		t.Errorf("Pause(null) sent %v", got)
	}
}

func TestSecondInstanceFocusesTheWindow(t *testing.T) {
	h := &fakeHost{}
	f := newFakeDaemon()
	a, _ := newTestApp(t, f, h)
	a.domReady()
	a.secondInstance(nil)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.focused != 1 {
		t.Errorf("Focus called %d times, want 1", h.focused)
	}
	if len(h.confirmIDs) != 0 {
		t.Errorf("confirmation requests %s without --confirm", h.confirmIDs)
	}
}

func TestConfirmArgs(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want []int64
	}{
		{nil, nil},
		{[]string{"--confirm", "42"}, []int64{42}},
		{[]string{"--confirm=7", "-confirm", "8", "--confirm", "7"}, []int64{7, 8}},
		{[]string{"x", "--other", "--confirm", "0", "--confirm", "-3", "--confirm", "abc", "--confirm=9223372036854775808"}, nil},
		{[]string{"confirm", "5", "--confirmed", "6", "--confirm"}, nil},
		{[]string{"--confirm", "12", "--confirm"}, []int64{12}},
	} {
		if got := confirmArgs(tc.args); !slices.Equal(got, tc.want) {
			t.Errorf("confirmArgs(%q) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

func TestConfirmRequestsReachThePage(t *testing.T) {
	h := &fakeHost{}
	a, _ := newTestApp(t, newFakeDaemon(), h)

	// The window's own "--confirm" waits until the page has loaded.
	a.requestConfirmations(confirmArgs([]string{"--confirm", "5"}))
	if got := h.confirmations(); len(got) != 0 {
		t.Fatalf("sent before the page was ready: %v", got)
	}
	a.domReady()
	if got := h.confirmations(); !slices.Equal(got, []string{`{"id":"5"}`}) {
		t.Fatalf("after domReady: %v", got)
	}

	// A second start hands its arguments over and brings the window forward.
	a.secondInstance([]string{"--confirm", "6", "--confirm=7"})
	if got := h.confirmations(); !slices.Equal(got, []string{`{"id":"5"}`, `{"id":"6"}`, `{"id":"7"}`}) {
		t.Errorf("after the second instance: %v", got)
	}
	h.mu.Lock()
	focused := h.focused
	h.mu.Unlock()
	if focused != 1 {
		t.Errorf("Focus called %d times, want 1", focused)
	}
	// Nothing is sent twice.
	a.domReady()
	if got := h.confirmations(); len(got) != 3 {
		t.Errorf("after a second domReady: %v", got)
	}
}

func awaitingDownload() *downexv1.Download {
	return &downexv1.Download{
		Id: 5, Url: "https://example.com/v.mp4", Dir: `C:\Downloads\Video`, Filename: "v.mp4", Size: -1,
		State: downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION, Source: downexv1.AddSource_ADD_SOURCE_BROWSER,
		ExpectedSize: 1 << 33, Mime: "video/mp4", Category: "Video", CreatedAt: ts("2026-10-20T09:00:00Z"),
	}
}

func TestAwaitingDownloadJSONShape(t *testing.T) {
	a, _ := newTestApp(t, newFakeDaemon(awaitingDownload()), nil)
	raw, err := a.List()
	if err != nil {
		t.Fatal(err)
	}
	d := decodeJSON(t, raw)["downloads"].([]any)[0].(map[string]any)
	want := map[string]any{
		"id": "5", "url": "https://example.com/v.mp4", "dir": `C:\Downloads\Video`, "filename": "v.mp4", "size": "-1",
		"state": "DOWNLOAD_STATE_AWAITING_CONFIRMATION", "source": "ADD_SOURCE_BROWSER",
		"expectedSize": "8589934592", "mime": "video/mp4", "category": "Video", "createdAt": "2026-10-20T09:00:00Z",
	}
	if !reflect.DeepEqual(d, want) {
		t.Errorf("download =\n%#v\nwant\n%#v", d, want)
	}
}

func TestAddForwardsTheHints(t *testing.T) {
	f := newFakeDaemon()
	a, _ := newTestApp(t, f, nil)
	if _, err := a.Add(json.RawMessage(`{"url":"https://example.com/a","source":"ADD_SOURCE_UI","expectedSize":"1024","mime":"application/zip"}`)); err != nil {
		t.Fatal(err)
	}
	want := &downexv1.AddRequest{Url: "https://example.com/a", Source: downexv1.AddSource_ADD_SOURCE_UI, ExpectedSize: 1024, Mime: "application/zip"}
	if got := f.lastRequest(t); !proto.Equal(got, want) {
		t.Errorf("daemon got %v, want %v", got, want)
	}
}

func TestConfirmPauseAllResumeAll(t *testing.T) {
	f := newFakeDaemon(awaitingDownload(), sampleDownload())
	a, _ := newTestApp(t, f, nil)

	raw, err := a.Confirm(json.RawMessage(`{"id":"5","dir":"D:\\Films","filename":"movie.mp4","start":true,"maxConnections":4,"speedLimit":0}`))
	if err != nil {
		t.Fatal(err)
	}
	got := f.lastRequest(t).(*downexv1.ConfirmRequest)
	if got.GetId() != 5 || got.GetDir() != `D:\Films` || got.GetFilename() != "movie.mp4" || !got.GetStart() ||
		got.MaxConnections == nil || got.GetMaxConnections() != 4 || got.SpeedLimit == nil || got.GetSpeedLimit() != 0 {
		t.Errorf("Confirm sent %v", got)
	}
	if d := decodeJSON(t, raw)["download"].(map[string]any); d["state"] != "DOWNLOAD_STATE_QUEUED" || d["dir"] != `D:\Films` {
		t.Errorf("ConfirmResponse = %s", raw)
	}

	// "Download later": start false is the default and may be left out.
	if _, err := a.Confirm(json.RawMessage(`{"id":5}`)); err != nil {
		t.Fatal(err)
	}
	if got := f.lastRequest(t).(*downexv1.ConfirmRequest); got.GetStart() || got.MaxConnections != nil || got.SpeedLimit != nil {
		t.Errorf("Confirm without options sent %v", got)
	}

	_, err = a.Confirm(json.RawMessage(`{"id":7,"start":true}`))
	if err == nil || !strings.HasPrefix(err.Error(), "failed_precondition: ") {
		t.Errorf("Confirm of a download that is not awaiting = %v", err)
	}
	_, err = a.Confirm(json.RawMessage(`{"id":5,"category":"Video"}`))
	if err == nil || !strings.HasPrefix(err.Error(), "invalid_argument: malformed request: ") {
		t.Errorf("Confirm with an unknown field = %v", err)
	}

	raw, err = a.PauseAll()
	if err != nil || string(raw) != `{"paused":2}` {
		t.Errorf("PauseAll = %s, %v", raw, err)
	}
	if got := f.lastRequest(t); !proto.Equal(got, &downexv1.PauseAllRequest{}) {
		t.Errorf("PauseAll sent %v", got)
	}
	raw, err = a.ResumeAll()
	if err != nil || string(raw) != `{}` {
		t.Errorf("ResumeAll = %s, %v (0 resumed is the default, omitted)", raw, err)
	}
	if got := f.lastRequest(t); !proto.Equal(got, &downexv1.ResumeAllRequest{}) {
		t.Errorf("ResumeAll sent %v", got)
	}
}

func TestSettingsRoundTripKeepsEveryField(t *testing.T) {
	f := newFakeDaemon()
	stored := &downexv1.Settings{
		MaxConcurrentDownloads: 3, DefaultDir: `C:\Users\me\Downloads`, DefaultMaxConnections: 8,
		Categories: []*downexv1.Category{
			{Name: "Video", Extensions: []string{"mp4", "mkv"}},
			{Name: "Books", Extensions: []string{"epub"}, Dir: `D:\Books`},
		},
		UseCategoryFolders: true, ConfirmBrowserDownloads: true, NotifyOnComplete: true, ShowTrayIcon: true,
	}
	// A field of a newer downexd that this window does not know (number 99, varint 7).
	future := protowire.AppendVarint(protowire.AppendTag(nil, 99, protowire.VarintType), 7)
	stored.ProtoReflect().SetUnknown(future)
	f.settings = stored
	a, _ := newTestApp(t, f, nil)

	raw, err := a.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	s := decodeJSON(t, raw)["settings"].(map[string]any)
	wantJSON := map[string]any{
		"maxConcurrentDownloads": float64(3), "defaultDir": `C:\Users\me\Downloads`, "defaultMaxConnections": float64(8),
		"categories": []any{
			map[string]any{"name": "Video", "extensions": []any{"mp4", "mkv"}},
			map[string]any{"name": "Books", "extensions": []any{"epub"}, "dir": `D:\Books`},
		},
		"useCategoryFolders": true, "confirmBrowserDownloads": true, "notifyOnComplete": true, "showTrayIcon": true,
	}
	if !reflect.DeepEqual(s, wantJSON) {
		t.Errorf("GetSettings =\n%#v\nwant\n%#v", s, wantJSON)
	}

	// The page writes every field back (false values explicitly); the unknown one is kept.
	_, err = a.UpdateSettings(json.RawMessage(`{"settings":{"maxConcurrentDownloads":4,"globalSpeedLimit":0,
		"defaultDir":"D:\\dl","defaultMaxConnections":8,
		"categories":[{"name":"Video","extensions":["mp4"],"dir":""},{"name":"Comics","extensions":["cbz","cbr"],"dir":"D:\\Comics"}],
		"useCategoryFolders":false,"confirmBrowserDownloads":true,"notifyOnComplete":false,"notifyOnFailure":true,
		"launchAtLogin":true,"showTrayIcon":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := &downexv1.Settings{
		MaxConcurrentDownloads: 4, DefaultDir: `D:\dl`, DefaultMaxConnections: 8,
		Categories: []*downexv1.Category{
			{Name: "Video", Extensions: []string{"mp4"}},
			{Name: "Comics", Extensions: []string{"cbz", "cbr"}, Dir: `D:\Comics`},
		},
		ConfirmBrowserDownloads: true, NotifyOnFailure: true, LaunchAtLogin: true,
	}
	want.ProtoReflect().SetUnknown(future)
	got := f.lastRequest(t).(*downexv1.UpdateSettingsRequest).GetSettings()
	if !proto.Equal(got, want) {
		t.Errorf("UpdateSettings sent %v, want %v", got, want)
	}

	// Without the current settings, nothing is replaced.
	f.mu.Lock()
	f.err = connect.NewError(connect.CodeUnavailable, errors.New("downexd is not running"))
	n := len(f.requests)
	f.mu.Unlock()
	if _, err := a.UpdateSettings(json.RawMessage(`{"settings":{"maxConcurrentDownloads":4}}`)); err == nil ||
		!strings.HasPrefix(err.Error(), "unavailable: ") {
		t.Errorf("UpdateSettings while unreachable = %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != n {
		t.Errorf("an update reached the daemon: %v", f.requests[n:])
	}
}
