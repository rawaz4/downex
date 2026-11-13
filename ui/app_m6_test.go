package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/rpc/downex/v1/downexv1connect"
)

func TestQueueBindingsJSONShape(t *testing.T) {
	f := newFakeDaemon()
	a, _ := newTestApp(t, f, nil)

	raw, err := a.CreateQueue(json.RawMessage(`{"queue":{"name":"Night","maxConcurrent":2,"speedLimit":"1048576",
		"schedule":{"enabled":true,"startMinute":1380,"stopEnabled":true,"stopMinute":420,"weekdays":31,"wakeComputer":true,"repeatMinutes":30},
		"postAction":"POST_ACTION_SHUTDOWN"}}`))
	if err != nil {
		t.Fatal(err)
	}
	wantReq := &downexv1.CreateQueueRequest{Queue: &downexv1.Queue{
		Name: "Night", MaxConcurrent: 2, SpeedLimit: 1 << 20,
		Schedule:   &downexv1.Schedule{Enabled: true, StartMinute: 1380, StopEnabled: true, StopMinute: 420, Weekdays: 31, WakeComputer: true, RepeatMinutes: 30},
		PostAction: downexv1.PostAction_POST_ACTION_SHUTDOWN,
	}}
	if got := f.lastRequest(t); !proto.Equal(got, wantReq) {
		t.Errorf("CreateQueue sent %v, want %v", got, wantReq)
	}
	q := decodeJSON(t, raw)["queue"].(map[string]any)
	if q["id"] != "2" || q["name"] != "Night" || q["postAction"] != "POST_ACTION_SHUTDOWN" || q["speedLimit"] != "1048576" {
		t.Errorf("CreateQueueResponse = %s", raw)
	}

	if _, err := a.MoveDownloads(json.RawMessage(`{"ids":["7","8"],"queueId":"2","position":-1}`)); err != nil {
		t.Fatal(err)
	}
	if got := f.lastRequest(t); !proto.Equal(got, &downexv1.MoveDownloadsRequest{Ids: []int64{7, 8}, QueueId: 2, Position: -1}) {
		t.Errorf("MoveDownloads sent %v", got)
	}
	raw, err = a.MoveDownloads(json.RawMessage(`{"ids":["8"],"queueId":"2","position":0}`))
	if err != nil {
		t.Fatal(err)
	}
	if ids := decodeJSON(t, raw)["queue"].(map[string]any)["downloadIds"]; !reflect.DeepEqual(ids, []any{"8", "7"}) {
		t.Errorf("order after the move = %v", ids)
	}

	if raw, err = a.StopQueue(json.RawMessage(`{"id":"1"}`)); err != nil {
		t.Fatal(err)
	}
	if _, running := decodeJSON(t, raw)["queue"].(map[string]any)["running"]; running {
		t.Errorf("StopQueue = %s, want running omitted (false)", raw)
	}
	if raw, err = a.StartQueue(json.RawMessage(`{"id":1}`)); err != nil || decodeJSON(t, raw)["queue"].(map[string]any)["running"] != true {
		t.Errorf("StartQueue = %s, %v", raw, err)
	}

	raw, err = a.ListQueues()
	if err != nil {
		t.Fatal(err)
	}
	list := decodeJSON(t, raw)["queues"].([]any)
	main := list[0].(map[string]any)
	if len(list) != 2 || main["isMain"] != true || main["id"] != "1" {
		t.Errorf("ListQueues = %s", raw)
	}
	night := list[1].(map[string]any)
	wantSchedule := map[string]any{"enabled": true, "startMinute": float64(1380), "stopEnabled": true, "stopMinute": float64(420),
		"weekdays": float64(31), "wakeComputer": true, "repeatMinutes": float64(30)}
	if !reflect.DeepEqual(night["schedule"], wantSchedule) {
		t.Errorf("schedule = %v, want %v", night["schedule"], wantSchedule)
	}

	// A full replacement; once mode (weekdays 0) with a date.
	if _, err := a.UpdateQueue(json.RawMessage(`{"queue":{"id":"2","name":"Weekend","maxConcurrent":0,"speedLimit":0,
		"schedule":{"enabled":true,"startMinute":60,"weekdays":0,"onceDate":"2026-10-24"},"postAction":"POST_ACTION_UNSPECIFIED"}}`)); err != nil {
		t.Fatal(err)
	}
	if got := f.lastRequest(t).(*downexv1.UpdateQueueRequest).GetQueue(); got.GetName() != "Weekend" ||
		got.GetSchedule().GetOnceDate() != "2026-10-24" || got.GetPostAction() != downexv1.PostAction_POST_ACTION_UNSPECIFIED {
		t.Errorf("UpdateQueue sent %v", got)
	}

	if _, err := a.DeleteQueue(json.RawMessage(`{"id":"1"}`)); err == nil || !strings.HasPrefix(err.Error(), "failed_precondition: ") {
		t.Errorf("DeleteQueue(main) = %v", err)
	}
	if raw, err := a.DeleteQueue(json.RawMessage(`{"id":"2"}`)); err != nil || string(raw) != "{}" {
		t.Errorf("DeleteQueue = %s, %v", raw, err)
	}
	if _, err := a.CreateQueue(json.RawMessage(`{"queue":{"name":"x","bogus":1}}`)); err == nil ||
		!strings.HasPrefix(err.Error(), "invalid_argument: malformed request: ") {
		t.Errorf("CreateQueue with an unknown field = %v", err)
	}

	if raw, err := a.CancelPostAction(json.RawMessage(`{"queueId":"3"}`)); err != nil || string(raw) != "{}" {
		t.Errorf("CancelPostAction = %s, %v", raw, err)
	}
	if got := f.lastRequest(t); !proto.Equal(got, &downexv1.CancelPostActionRequest{QueueId: 3}) {
		t.Errorf("CancelPostAction sent %v", got)
	}
}

func TestSecretBindings(t *testing.T) {
	f := newFakeDaemon()
	a, _ := newTestApp(t, f, nil)
	if raw, err := a.SetSecret(json.RawMessage(`{"kind":"site_login","id":"L1","password":"s3cr\u00e9t"}`)); err != nil || string(raw) != "{}" {
		t.Fatalf("SetSecret = %s, %v", raw, err)
	}
	if got := f.lastRequest(t); !proto.Equal(got, &downexv1.SetSecretRequest{Kind: "site_login", Id: "L1", Password: "s3crét"}) {
		t.Errorf("SetSecret sent %v", got)
	}
	if _, err := a.ClearSecret(json.RawMessage(`{"kind":"proxy"}`)); err != nil {
		t.Fatal(err)
	}
	if got := f.lastRequest(t); !proto.Equal(got, &downexv1.ClearSecretRequest{Kind: "proxy"}) {
		t.Errorf("ClearSecret sent %v", got)
	}
	// A malformed request is refused without quoting it: it may hold the password.
	n := len(f.requests)
	for _, bad := range []string{
		`{"kind":"proxy","password":12345678}`,
		`{"kind":"proxy","password":"hunter2-secret","extra":"hunter2-secret"}`,
		`{"kind":"proxy","password":"hunter2-secret"`,
	} {
		_, err := a.SetSecret(json.RawMessage(bad))
		if err == nil || err.Error() != "invalid_argument: malformed request" {
			t.Errorf("SetSecret(%s) = %v", bad, err)
		}
		if err != nil && (strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "12345678")) {
			t.Errorf("the error repeats the password: %v", err)
		}
	}
	if len(f.requests) != n {
		t.Errorf("a malformed SetSecret reached the daemon: %v", f.requests[n:])
	}
	// The daemon's refusal arrives as Connect text.
	if _, err := a.SetSecret(json.RawMessage(`{"kind":"other","password":"x"}`)); err == nil || !strings.HasPrefix(err.Error(), "invalid_argument: kind") {
		t.Errorf("SetSecret with a bad kind = %v", err)
	}
}

func TestUpdateBindings(t *testing.T) {
	f := newFakeDaemon()
	a, _ := newTestApp(t, f, nil)
	if _, err := a.CheckForUpdate(); err == nil || !strings.HasPrefix(err.Error(), "failed_precondition: updates are off") {
		t.Errorf("CheckForUpdate without a feed = %v", err)
	}
	f.mu.Lock()
	f.updateInfo = &downexv1.UpdateInfo{CurrentVersion: "0.3.3", LatestVersion: "0.4.0", Available: true, Notes: "Queues",
		PublishedAt: ts("2026-10-20T08:00:00Z"), CheckedAt: ts("2026-10-21T09:30:00Z")}
	f.mu.Unlock()
	raw, err := a.CheckForUpdate()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"info": map[string]any{"currentVersion": "0.3.3", "latestVersion": "0.4.0", "available": true, "notes": "Queues",
		"publishedAt": "2026-10-20T08:00:00Z", "checkedAt": "2026-10-21T09:30:00Z"}}
	if got := decodeJSON(t, raw); !reflect.DeepEqual(got, want) {
		t.Errorf("CheckForUpdate = %v, want %v", got, want)
	}
	if raw, err := a.InstallUpdate(); err != nil || decodeJSON(t, raw)["info"].(map[string]any)["latestVersion"] != "0.4.0" {
		t.Errorf("InstallUpdate = %s, %v", raw, err)
	}
	if got := f.lastRequest(t); !proto.Equal(got, &downexv1.InstallUpdateRequest{}) {
		t.Errorf("InstallUpdate sent %v", got)
	}
}

func TestPickFile(t *testing.T) {
	h := &fakeHost{pickedFile: `C:\Tools\scan.exe`}
	a, _ := newTestApp(t, newFakeDaemon(), h)
	dir := t.TempDir()
	got, err := a.PickFile(json.RawMessage(`{"title":"Choose the scanner","initialDir":` + jsonString(dir+`/missing/scan.exe`) + `}`))
	if err != nil || got != `C:\Tools\scan.exe` {
		t.Fatalf("PickFile = %q, %v", got, err)
	}
	if got, err := a.PickFile(nil); err != nil || got != `C:\Tools\scan.exe` {
		t.Errorf("PickFile(null) = %q, %v", got, err)
	}
	h.mu.Lock()
	picks := slices.Clone(h.filePicks)
	h.mu.Unlock()
	// The starting folder is the nearest existing one; without a title the picker has one.
	if want := []string{"Choose the scanner\n" + dir, "Choose a file\n"}; !slices.Equal(picks, want) {
		t.Errorf("pickers %q, want %q", picks, want)
	}
	if _, err := a.PickFile(json.RawMessage(`{"filters":[]}`)); err == nil || !strings.HasPrefix(err.Error(), "invalid_argument: malformed request") {
		t.Errorf("PickFile with an unknown field = %v", err)
	}
	idle, _ := newTestApp(t, newFakeDaemon(), nil)
	if _, err := idle.PickFile(nil); err == nil || !strings.HasPrefix(err.Error(), "unavailable:") {
		t.Errorf("PickFile without a window = %v", err)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// Every M6 setting reaches the daemon as the page wrote it, and fields of a newer daemon
// that this window does not know survive the replacement, nested ones included.
func TestSettingsRoundTripKeepsM6Fields(t *testing.T) {
	f := newFakeDaemon()
	future := protowire.AppendVarint(protowire.AppendTag(nil, 99, protowire.VarintType), 7)
	stored := &downexv1.Settings{
		MaxConcurrentDownloads: 3, DefaultDir: `C:\dl`, DefaultMaxConnections: 8,
		Proxy:      &downexv1.ProxySettings{Mode: downexv1.ProxyMode_PROXY_MODE_MANUAL, HttpProxy: "proxy:8080", Username: "me", HasPassword: true},
		SiteLogins: []*downexv1.SiteLogin{{Id: "a1", UrlPattern: "https://example.com/*", Username: "u", HasPassword: true}},
	}
	stored.GetProxy().ProtoReflect().SetUnknown(future)
	stored.GetSiteLogins()[0].ProtoReflect().SetUnknown(future)
	f.settings = stored
	a, _ := newTestApp(t, f, nil)

	raw, err := a.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	s := decodeJSON(t, raw)["settings"].(map[string]any)
	if p := s["proxy"].(map[string]any); p["mode"] != "PROXY_MODE_MANUAL" || p["hasPassword"] != true || p["username"] != "me" {
		t.Errorf("proxy = %v", p)
	}

	_, err = a.UpdateSettings(json.RawMessage(`{"settings":{"maxConcurrentDownloads":3,"globalSpeedLimit":0,"defaultDir":"C:\\dl",
		"defaultMaxConnections":8,"categories":[],"useCategoryFolders":true,"confirmBrowserDownloads":true,
		"notifyOnComplete":true,"notifyOnFailure":true,"launchAtLogin":false,"showTrayIcon":true,"showProgressWindow":true,
		"showCompleteDialog":true,
		"proxy":{"mode":"PROXY_MODE_PAC","httpProxy":"proxy:8080","httpsProxy":"","socksProxy":"socks5://s:1080","bypass":"<local>;*.lan",
			"pacUrl":"https://wpad.example/proxy.pac","username":"me","hasPassword":true},
		"hostRules":[{"pattern":"*.example.com","maxConnections":4,"speedLimit":"2097152"}],
		"siteLogins":[{"id":"a1","urlPattern":"https://example.com/*","username":"u2","hasPassword":true},
			{"id":"","urlPattern":"*.example.org","username":"new","hasPassword":false}],
		"clipboardMonitor":true,"markDownloadedFiles":false,"avScanEnabled":true,"avScanCommand":"C:\\Tools\\scan.exe",
		"avScanArgs":"--quiet [File]","updateFeedUrl":"https://updates.example/feed.json","autoCheckUpdates":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := &downexv1.Settings{
		MaxConcurrentDownloads: 3, DefaultDir: `C:\dl`, DefaultMaxConnections: 8,
		UseCategoryFolders: true, ConfirmBrowserDownloads: true, NotifyOnComplete: true, NotifyOnFailure: true,
		ShowTrayIcon: true, ShowProgressWindow: true, ShowCompleteDialog: true,
		Proxy: &downexv1.ProxySettings{Mode: downexv1.ProxyMode_PROXY_MODE_PAC, HttpProxy: "proxy:8080", SocksProxy: "socks5://s:1080",
			Bypass: "<local>;*.lan", PacUrl: "https://wpad.example/proxy.pac", Username: "me", HasPassword: true},
		HostRules: []*downexv1.HostRule{{Pattern: "*.example.com", MaxConnections: 4, SpeedLimit: 2 << 20}},
		SiteLogins: []*downexv1.SiteLogin{
			{Id: "a1", UrlPattern: "https://example.com/*", Username: "u2", HasPassword: true},
			{UrlPattern: "*.example.org", Username: "new"},
		},
		ClipboardMonitor: true, AvScanEnabled: true, AvScanCommand: `C:\Tools\scan.exe`, AvScanArgs: "--quiet [File]",
		UpdateFeedUrl: "https://updates.example/feed.json",
	}
	want.GetProxy().ProtoReflect().SetUnknown(future)
	want.GetSiteLogins()[0].ProtoReflect().SetUnknown(future)
	got := f.lastRequest(t).(*downexv1.UpdateSettingsRequest).GetSettings()
	if !proto.Equal(got, want) {
		t.Errorf("UpdateSettings sent\n%v\nwant\n%v", got, want)
	}
	if u := got.GetSiteLogins()[1].ProtoReflect().GetUnknown(); len(u) != 0 {
		t.Errorf("a new site login got the unknown fields of another: %v", u)
	}
}

// Queue and post-action events are forwarded as they come, in the protojson shape.
func TestBridgeForwardsQueueEvents(t *testing.T) {
	queueEvents := &downexv1.WatchResponse{Events: []*downexv1.Event{
		{Kind: &downexv1.Event_QueueUpdated{QueueUpdated: &downexv1.Queue{Id: 2, Name: "Night", Running: true, DownloadIds: []int64{5, 4}}}},
		{Kind: &downexv1.Event_QueueRemoved{QueueRemoved: 3}},
		{Kind: &downexv1.Event_PostAction{PostAction: &downexv1.PostActionPending{
			QueueId: 2, Action: downexv1.PostAction_POST_ACTION_SLEEP, Deadline: ts("2026-10-21T23:01:00Z"),
		}}},
	}}
	sw := &scriptedWatch{sessions: []func(context.Context) (WatchStream, error){stream(nil, snapshotOf(4, 5), queueEvents)}}
	f := newFakeDaemon()
	f.watch = sw.watch
	h := &fakeHost{emitted: make(chan json.RawMessage, 16)}
	newTestApp(t, f, h)
	if got := eventKinds(t, nextEvent(t, h.emitted)); !slices.Equal(got, []string{"snapshot:4", "snapshot:5"}) {
		t.Fatalf("snapshot = %v", got)
	}
	got := nextEvent(t, h.emitted)
	want := map[string]any{"events": []any{
		map[string]any{"queueUpdated": map[string]any{"id": "2", "name": "Night", "running": true, "downloadIds": []any{"5", "4"}}},
		map[string]any{"queueRemoved": "3"},
		map[string]any{"postAction": map[string]any{"queueId": "2", "action": "POST_ACTION_SLEEP", "deadline": "2026-10-21T23:01:00Z"}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("queue events =\n%v\nwant\n%v", got, want)
	}
}

// queueServer is QueueService as an httptest server serves it.
type queueServer struct {
	downexv1connect.UnimplementedQueueServiceHandler
}

func (queueServer) ListQueues(context.Context, *connect.Request[downexv1.ListQueuesRequest]) (*connect.Response[downexv1.ListQueuesResponse], error) {
	return connect.NewResponse(&downexv1.ListQueuesResponse{Queues: []*downexv1.Queue{{Id: 1, Name: "Main queue", IsMain: true, Running: true}}}), nil
}

func (queueServer) StartQueue(_ context.Context, req *connect.Request[downexv1.StartQueueRequest]) (*connect.Response[downexv1.StartQueueResponse], error) {
	return nil, connect.NewError(connect.CodeNotFound, errors.New("queue not found"))
}

// The real client reaches QueueService on the daemon's endpoint and the M6 DaemonService
// methods on its daemon client.
func TestClientDaemonQueueService(t *testing.T) {
	sys := &fakeSystem{}
	sys.startByUser()
	env := sys.env(t)
	mux := http.NewServeMux()
	mux.Handle(downexv1connect.NewQueueServiceHandler(queueServer{}))
	mux.Handle(downexv1connect.NewDaemonServiceHandler(daemonServer{}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	env.BaseURL = srv.URL
	env.HTTPClient = func(string) connect.HTTPClient { return srv.Client() }
	a, _ := newTestApp(t, newClientDaemon(env), nil)

	raw, err := a.ListQueues()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"queues": []any{map[string]any{"id": "1", "name": "Main queue", "running": true, "isMain": true}}}
	if got := decodeJSON(t, raw); !reflect.DeepEqual(got, want) {
		t.Errorf("ListQueues = %s, want %v", raw, want)
	}
	if _, err := a.StartQueue(json.RawMessage(`{"id":9}`)); err == nil || err.Error() != "not_found: queue not found" {
		t.Errorf("StartQueue = %v", err)
	}
	// An older downexd without the M6 methods answers unimplemented.
	if _, err := a.CheckForUpdate(); err == nil || !strings.HasPrefix(err.Error(), "unimplemented: ") {
		t.Errorf("CheckForUpdate on a daemon without it = %v", err)
	}
	if n := sys.startCount(); n != 0 {
		t.Errorf("downexd started %d times while running", n)
	}
}
