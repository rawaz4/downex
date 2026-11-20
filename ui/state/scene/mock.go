// Package scene is the simulated daemon of today's demo and tests (ui/frontend/src/lib/mock.ts),
// ported so that every native screen can render the states of the step-1 references: the
// Studio scene of DESIGN-SYSTEM.md §7.1, a generated list of N rows, the layout fixtures of
// scripts/measure-dialogs.mjs (87 states, Fixtures) and the progress-window views.
//
// Mock implements host.Daemon, so a native window can run on it as it runs on downexd, and
// state.Events, so the stores can subscribe to it directly. It follows DAEMON-SPEC §3–§6
// closely enough to exercise every UI path: a FIFO queue bounded by
// max_concurrent_downloads, probing, segmented transfers with dynamic splitting,
// per-download, per-queue and global speed limits, failures with stable error codes and
// event batches at the daemon's 4-per-second cadence. The same seed and options give the
// same simulation as the TypeScript one.
//
// URL markers trigger scenarios as in today's mock (see mock.ts's header): "404", "changed",
// "flaky", "unknown-size", "norange", "size=<bytes>"; streams: "drm"/"encrypted", "live",
// "404", "audio"/"podcast", "muxed", "init-protected"; video pages: "drm", "live",
// "/playlist", "podcast"/"audio-only", "private"/"unavailable", "age"/"signin",
// "unsupported", "broken", "dub", "film"/"movie", and a fixture page's
// "qualities=", "audio=", "title=".
package scene

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/confirm"
	"downex/ui/host"
	"downex/ui/state"
)

// Options configure a Mock (MockOptions).
type Options struct {
	// Seed of the generator (0: 0x5eed); the same seed and options give the same simulation.
	Seed uint32
	// Count of generated downloads (Scene Random: 0 is an empty list); Studio ignores it.
	Count int
	// Scene: Studio (the default without a count) or Random (the default with one).
	Scene Kind
	// FreeBytes on every simulated volume at start (0: 214 GiB); completions use it up.
	FreeBytes float64
	// AutoTick runs the simulation on a timer (TickMs); tests call Tick instead.
	AutoTick bool
	// TickMs is the step and event batch interval (0: 250 ms = 4 batches/s).
	TickMs float64
	// Latency of every call (the demo uses 40 ms).
	Latency time.Duration
	// Now is the clock (nil: time.Now).
	Now func() time.Time
	// DefaultDir (""; a Windows-looking "C:\Users\you\Downloads").
	DefaultDir string
	// FailureRate is the chance that a started download hits a network error part way
	// (nil: 0.05).
	FailureRate *float64
	// HandoffEvery is the interval of simulated browser hand-offs while the timer runs
	// (0: 90 s, the first 6 s after start; negative: none; none with a fixture).
	HandoffEvery time.Duration
	// RetryRate is how often a receiving connection waits and retries, per second (nil:
	// 0.006).
	RetryRate *float64
	// AssembleMs is how long a media download assembles its fragments (nil: 1500).
	AssembleMs *float64
	// View is what Startup reports (nil: the main window with Open with…, hibernation and
	// wake timers).
	View *Startup
	// Rename shows some Studio downloads under other names (old name -> new), for pictures
	// meant for people (the README's and the website's); tests keep the scene's names.
	Rename map[string]string
	// ToolsReady: yt-dlp and Deno are installed at start.
	ToolsReady bool
	// ToolsInstallMs is how long their download takes (nil: 4000).
	ToolsInstallMs *float64
	// ToolsFail: their download fails.
	ToolsFail bool
	// ResolveMs is how long yt-dlp takes to read a page (nil: 1200; 0 = at once).
	ResolveMs *float64
	// Settings changes the default settings.
	Settings func(*downexv1.Settings)
	// Fixture is a layout fixture: comma-separated tokens on top of the Studio scene (see
	// ApplyFixture). With one, the simulated browser hands nothing over by itself.
	Fixture string
	// OnOpenProgressWindow and OnCloseWindow are called by those window requests.
	OnOpenProgressWindow func(id int64)
	OnCloseWindow        func()
}

// Kind is the list a Mock holds at start.
type Kind uint8

const (
	// DefaultScene: Studio without a Count, Random with one.
	DefaultScene Kind = iota
	// Studio is the fixed scene of the design's main window: three downloads running on
	// 16, 8 and 4 connections (one a YouTube video), one to confirm, one paused, seven
	// queued, one failed with HTTP 503, an HLS stream and fourteen more completed.
	Studio
	// Random is Count generated rows.
	Random
)

// Startup is what the window's start-up reports (api.ts StartupView).
type Startup struct {
	Progress       int64 // 0: the main window
	OpenWith       bool
	Hibernate      bool
	WakeTimers     bool
	CustomTitleBar bool
	// Build is the window's build (nil: the demo's own, BuildOf).
	Build *host.BuildInfo
}

// WindowState is what the page asked of its simulated window.
type WindowState struct {
	Title     string
	Width     float64
	Height    float64
	Sizes     [][2]float64
	Closed    bool
	Opened    []int64
	Minimised int
	Maximised bool
	Themes    []string
	Folders   []string
	// Registered counts the page's registrations with the browsers.
	Registered int
}

type slot struct {
	index    float64
	state    downexv1.ConnectionState
	phaseMs  float64
	reason   string
	seg      *segment
	last     segment
	speedBps float64
}

type mediaSim struct {
	probed     *downexv1.MediaInfo
	totalBytes float64
}

type sim struct {
	d                     *dl
	baseSpeed, wobble     float64
	probeLeftMs           float64
	failAt                float64
	failCode, failMessage string
	probeError            string
	hiddenSize            float64
	remoteChanged         bool
	changeAt              float64
	scenarioDone          bool
	noRange               bool
	pendingSize           float64
	media                 *mediaSim
	slots                 []*slot
	assembleLeftMs        float64
	replaceBytes          float64
}

type mqueue struct {
	id               int64
	name             string
	running          bool
	maxConcurrent    int32
	speedLimit       int64
	schedule         *downexv1.Schedule
	postAction       downexv1.PostAction
	isMain           bool
	order            []int64
	completedThisRun bool
}

type countdown struct {
	action          downexv1.PostAction
	deadline, abort float64
}

type resolvedPage struct {
	at   float64
	info *downexv1.MediaInfo
	err  error
}

type toolsWaiter chan error

// Mock is the simulated daemon. It is safe for concurrent use.
type Mock struct {
	opts Options

	mu         sync.Mutex
	rng        func() float64
	slotRng    func() float64
	now        func() float64
	tickMs     float64
	latency    time.Duration
	failRate   float64
	retryRate  float64
	assembleMs float64

	toolsInstallMs float64
	resolveMs      float64
	addSlowUntil   float64
	startedAt      float64
	settings       *downexv1.Settings
	sep            string

	sims     map[int64]*sim
	simOrder []int64
	changed  []int64
	changedS map[int64]bool
	removed  []int64
	nextID   int64

	queues        map[int64]*mqueue
	changedQueues []int64
	changedQS     map[int64]bool
	removedQueues []int64
	postEvents    []*downexv1.PostActionPending
	countdowns    map[int64]*countdown
	nextQueueID   int64

	proxyPassword  bool
	loginPasswords map[string]bool
	nextLogin      int
	queueDirty     bool

	toolsPhase     string // missing, installing, ready, failed
	toolsLeftMs    float64
	toolsCheckedAt float64
	toolsChanged   bool
	// Polite mode (docs/POLITE-SPEC.md): the status the fixture pins (nil: it follows the
	// setting, WATCHING while on), and whether it changed since the last batch.
	politeFixed   *downexv1.PoliteStatus
	politeChanged bool
	toolsWaiters  []toolsWaiter

	resolving    map[string]float64
	resolveOrder []string
	resolved     map[string]resolvedPage

	window         WindowState
	freeAtStart    float64
	completedBytes float64
	gone           map[int64]bool
	// pendingConfirms are the downloads a call made await confirmation (told after it).
	pendingConfirms []int64
	confirmRefusal  error

	subs        map[int]func(state.Batch)
	subOrder    []int
	nextSub     int
	confirmSubs map[int]func(int64)
	nextConfirm int

	stop     chan struct{}
	stopOnce sync.Once
	handoffs int
}

// New makes the simulated daemon; with Options.AutoTick it runs until Close.
func New(o Options) (*Mock, error) {
	seed := o.Seed
	if seed == 0 {
		seed = 0x5eed
	}
	m := &Mock{opts: o, rng: prng(seed), slotRng: prng(seed ^ 0x2f6b9c1d), latency: o.Latency,
		failRate: 0.05, retryRate: 0.006, assembleMs: 1500, toolsInstallMs: 4000, resolveMs: 1200,
		sims: map[int64]*sim{}, changedS: map[int64]bool{}, nextID: 1, queues: map[int64]*mqueue{},
		changedQS: map[int64]bool{}, countdowns: map[int64]*countdown{}, nextQueueID: 1,
		loginPasswords: map[string]bool{}, nextLogin: 1, queueDirty: true, toolsPhase: "missing",
		resolving: map[string]float64{}, resolved: map[string]resolvedPage{}, gone: map[int64]bool{},
		subs: map[int]func(state.Batch){}, confirmSubs: map[int]func(int64){}, stop: make(chan struct{})}
	clock := o.Now
	if clock == nil {
		clock = time.Now
	}
	m.now = func() float64 { return float64(clock().UnixMilli()) }
	m.tickMs = o.TickMs
	if m.tickMs <= 0 {
		m.tickMs = 250
	}
	if o.FailureRate != nil {
		m.failRate = *o.FailureRate
	}
	if o.RetryRate != nil {
		m.retryRate = *o.RetryRate
	}
	if o.AssembleMs != nil {
		m.assembleMs = *o.AssembleMs
	}
	if o.ToolsInstallMs != nil {
		m.toolsInstallMs = *o.ToolsInstallMs
	}
	if o.ResolveMs != nil {
		m.resolveMs = *o.ResolveMs
	}
	if o.ToolsReady {
		m.toolsPhase = "ready"
	}
	m.startedAt = m.now()
	if o.ToolsReady {
		m.toolsCheckedAt = m.startedAt
	}
	m.settings = state.DefaultSettings()
	m.settings.DefaultDir = o.DefaultDir
	if m.settings.DefaultDir == "" {
		m.settings.DefaultDir = `C:\Users\you\Downloads`
	}
	if o.Settings != nil {
		o.Settings(m.settings)
	}
	m.sep = "/"
	if strings.Contains(m.settings.GetDefaultDir(), `\`) {
		m.sep = `\`
	}
	m.window = WindowState{Width: 1200, Height: 760}
	m.freeAtStart = o.FreeBytes
	if m.freeAtStart == 0 {
		m.freeAtStart = 214 * GiB
	}

	m.newQueue("Main queue", 0, 0, &downexv1.Schedule{}, downexv1.PostAction_POST_ACTION_UNSPECIFIED, true, true)
	m.newQueue("Night", 2, 0, &downexv1.Schedule{Enabled: true, StartMinute: 60, StopEnabled: true, StopMinute: 420, Weekdays: 0x7f},
		downexv1.PostAction_POST_ACTION_UNSPECIFIED, false, false)

	kind := o.Scene
	if kind == DefaultScene {
		kind = Studio
		if o.Count > 0 {
			kind = Random
		}
	}
	if kind == Studio {
		m.seedStudio()
	} else {
		m.seed(max(0, o.Count))
	}
	if o.Fixture != "" {
		if err := m.applyFixture(o.Fixture); err != nil {
			return nil, err
		}
	}
	// the state at start is what the first read sees
	m.changedQueues, m.changedQS = nil, map[int64]bool{}

	if o.AutoTick {
		go m.run()
		every := o.HandoffEvery
		if every == 0 {
			every = 90 * time.Second
			if o.Fixture != "" {
				every = -1
			}
		}
		if every > 0 {
			go m.handoffLoop(every)
		}
	}
	return m, nil
}

// MustNew is New for a scene known to be valid (it panics on a bad fixture).
func MustNew(o Options) *Mock {
	m, err := New(o)
	if err != nil {
		panic(err)
	}
	return m
}

func (m *Mock) run() {
	t := time.NewTicker(time.Duration(m.tickMs * float64(time.Millisecond)))
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.Tick(m.tickMs)
		}
	}
}

func (m *Mock) handoffLoop(every time.Duration) {
	wait := min(6*time.Second, every)
	for {
		select {
		case <-m.stop:
			return
		case <-time.After(wait):
		}
		m.mu.Lock()
		odd := m.handoffs%2 == 1
		m.handoffs++
		m.mu.Unlock()
		if odd {
			_, _ = m.BrowserMediaHandoff(nil)
		} else {
			_, _ = m.BrowserHandoff(nil)
		}
		wait = every
	}
}

// Close stops the timers and drops the subscribers.
func (m *Mock) Close() {
	m.stopOnce.Do(func() { close(m.stop) })
	m.mu.Lock()
	for _, w := range m.toolsWaiters {
		close(w)
	}
	m.toolsWaiters = nil
	m.subs, m.subOrder = map[int]func(state.Batch){}, nil
	m.confirmSubs = map[int]func(int64){}
	m.mu.Unlock()
}

// ---- helpers ----------------------------------------------------------------------------

func (m *Mock) between(lo, hi float64) float64 { return lo + m.rng()*(hi-lo) }

func pick[T any](m *Mock, items []T) T { return items[int(floor(m.rng()*float64(len(items))))] }

func (m *Mock) list() []*sim {
	out := make([]*sim, 0, len(m.simOrder))
	for _, id := range m.simOrder {
		if s := m.sims[id]; s != nil {
			out = append(out, s)
		}
	}
	return out
}

func (m *Mock) addSim(s *sim) {
	m.sims[s.d.id] = s
	m.simOrder = append(m.simOrder, s.d.id)
}

func (m *Mock) deleteSim(id int64) {
	delete(m.sims, id)
	m.simOrder = slices.DeleteFunc(m.simOrder, func(x int64) bool { return x == id })
}

func (m *Mock) mark(id int64) {
	if !m.changedS[id] {
		m.changedS[id] = true
		m.changed = append(m.changed, id)
	}
}

func (m *Mock) touch(d *dl) {
	d.updatedAt = max(m.now(), d.updatedAt)
	m.mark(d.id)
}

func (m *Mock) markQueue(id int64) {
	if !m.changedQS[id] {
		m.changedQS[id] = true
		m.changedQueues = append(m.changedQueues, id)
	}
}

func (m *Mock) unmarkQueue(id int64) {
	if m.changedQS[id] {
		delete(m.changedQS, id)
		m.changedQueues = slices.DeleteFunc(m.changedQueues, func(x int64) bool { return x == id })
	}
}

func (m *Mock) newSim(d *dl) *sim {
	speed := m.between(0.4*MiB, 14*MiB)
	if m.rng() < 0.15 {
		speed *= 0.1
	}
	return &sim{d: d, baseSpeed: speed, wobble: 1, failAt: -1, hiddenSize: -1, changeAt: -1, pendingSize: -1, assembleLeftMs: -1, replaceBytes: -1}
}

// chooseCategory is the category the daemon would choose (DESKTOP-SPEC §2): by the
// extension of the requested name, else of the URL's last path segment, else by the MIME
// hint.
func (m *Mock) chooseCategory(filename, rawURL, mime string) string {
	for _, name := range []string{filename, nameFromURL(rawURL)} {
		ext := categories.ExtensionOf(name)
		if ext == "" {
			continue
		}
		for _, c := range m.settings.GetCategories() {
			if slices.Contains(c.GetExtensions(), ext) {
				return c.GetName()
			}
		}
	}
	if kind, ok := kindOfMime(mime); ok {
		for _, c := range m.settings.GetCategories() {
			if categories.CategoryKind(c.GetName(), c.GetExtensions()) == kind {
				return c.GetName()
			}
		}
	}
	return ""
}

func (m *Mock) categoryFolder(category string) string {
	return confirm.CategoryFolder(m.settings, category)
}

func (m *Mock) baseDownload(rawURL, filename, dir string, size, createdAt float64) *dl {
	d := &dl{id: m.nextID, url: rawURL, dir: dir, filename: filename, size: size, state: state.StateQueued,
		createdAt: createdAt, updatedAt: createdAt, source: downexv1.AddSource_ADD_SOURCE_UI,
		kind: downexv1.DownloadKind_DOWNLOAD_KIND_FILE, queueID: state.MainQueueID}
	m.nextID++
	return d
}

// ---- queues -----------------------------------------------------------------------------

func (m *Mock) newQueue(name string, maxConcurrent int32, speedLimit int64, sched *downexv1.Schedule, action downexv1.PostAction, isMain, running bool) *mqueue {
	q := &mqueue{id: m.nextQueueID, name: name, running: running, maxConcurrent: maxConcurrent, speedLimit: speedLimit,
		schedule: cloneSchedule(sched), postAction: action, isMain: isMain}
	m.nextQueueID++
	m.queues[q.id] = q
	m.markQueue(q.id)
	return q
}

func cloneSchedule(s *downexv1.Schedule) *downexv1.Schedule {
	if s == nil {
		return &downexv1.Schedule{}
	}
	return proto.CloneOf(s)
}

func (q *mqueue) clone() *downexv1.Queue {
	return &downexv1.Queue{Id: q.id, Name: q.name, Running: q.running, MaxConcurrent: q.maxConcurrent, SpeedLimit: q.speedLimit,
		Schedule: cloneSchedule(q.schedule), PostAction: q.postAction, IsMain: q.isMain, DownloadIds: slices.Clone(q.order)}
}

func (m *Mock) getQueue(id int64) (*mqueue, error) {
	q := m.queues[id]
	if q == nil {
		return nil, apiErr(connect.CodeNotFound, "queue "+strconv.FormatInt(id, 10)+" not found")
	}
	return q, nil
}

// targetQueue is the queue a request names (0 = the main queue).
func (m *Mock) targetQueue(id int64) (*mqueue, error) {
	if id <= 0 {
		id = state.MainQueueID
	}
	return m.getQueue(id)
}

// enqueue puts a download at the end of a queue (out of the one it was in).
func (m *Mock) enqueue(d *dl, q *mqueue) {
	if old := m.queues[d.queueID]; old != nil && old != q {
		old.order = slices.DeleteFunc(slices.Clone(old.order), func(x int64) bool { return x == d.id })
		m.markQueue(old.id)
	}
	if !slices.Contains(q.order, d.id) {
		q.order = append(q.order, d.id)
	}
	d.queueID = q.id
	m.markQueue(q.id)
}

func (m *Mock) queuesInOrder() []*mqueue {
	out := make([]*mqueue, 0, len(m.queues))
	for _, q := range m.queues {
		out = append(out, q)
	}
	slices.SortFunc(out, func(a, b *mqueue) int {
		if a.isMain != b.isMain {
			if a.isMain {
				return -1
			}
			return 1
		}
		return int(a.id - b.id)
	})
	return out
}

func isRunningState(d *dl) bool {
	return d.state == state.StateDownloading || d.state == state.StateProbing
}

// checkFinishedQueues counts down a queue's action when it ran out of work after
// completing something.
func (m *Mock) checkFinishedQueues() {
	t := m.now()
	for _, q := range m.queuesInOrder() {
		if !q.running || q.postAction == downexv1.PostAction_POST_ACTION_UNSPECIFIED || !q.completedThisRun || m.countdowns[q.id] != nil {
			continue
		}
		busy := slices.ContainsFunc(q.order, func(id int64) bool {
			s := m.sims[id]
			return s != nil && (s.d.state == state.StateQueued || isRunningState(s.d))
		})
		if busy {
			continue
		}
		q.completedThisRun = false
		deadline := t + 60_000
		m.countdowns[q.id] = &countdown{action: q.postAction, deadline: deadline, abort: deadline + 3000}
		m.postEvents = append(m.postEvents, &downexv1.PostActionPending{QueueId: q.id, Action: q.postAction, Deadline: stamp(deadline)})
	}
	ids := make([]int64, 0, len(m.countdowns))
	for id := range m.countdowns {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		c := m.countdowns[id]
		// The simulated daemon cannot act: it reports the action as aborted.
		if t >= c.abort {
			delete(m.countdowns, id)
			m.postEvents = append(m.postEvents, &downexv1.PostActionPending{QueueId: id, Action: c.action, Deadline: stamp(c.deadline), Cancelled: true})
		}
	}
}

// renumberQueue sets queue_position: 1-based among the queued downloads, in admission order.
func (m *Mock) renumberQueue() {
	m.queueDirty = false
	pos := 0.0
	seen := map[int64]bool{}
	for _, q := range m.queuesInOrder() {
		for _, id := range q.order {
			s := m.sims[id]
			if s == nil {
				continue
			}
			seen[id] = true
			want := 0.0
			if s.d.state == state.StateQueued {
				pos++
				want = pos
			}
			if s.d.queuePosition != want {
				s.d.queuePosition = want
				m.mark(id)
			}
		}
	}
	for _, s := range m.list() {
		if !seen[s.d.id] && s.d.queuePosition != 0 {
			s.d.queuePosition = 0
			m.mark(s.d.id)
		}
	}
}

// ---- subscribers ------------------------------------------------------------------------

// Subscribe implements state.Events: f receives a snapshot of every download and the tools'
// state at once (as Watch without ids), then every batch.
func (m *Mock) Subscribe(f func(state.Batch)) (cancel func()) {
	m.mu.Lock()
	id := m.nextSub
	m.nextSub++
	m.subs[id] = f
	m.subOrder = append(m.subOrder, id)
	snap := m.snapshotLocked()
	m.mu.Unlock()
	f(snap)
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(m.subs, id)
		m.subOrder = slices.DeleteFunc(m.subOrder, func(x int) bool { return x == id })
	}
}

func (m *Mock) snapshotLocked() state.Batch {
	var out state.Batch
	for _, s := range m.list() {
		out = append(out, &downexv1.Event{Kind: &downexv1.Event_Snapshot{Snapshot: s.d.clone()}})
	}
	out = append(out, &downexv1.Event{Kind: &downexv1.Event_Tools{Tools: m.toolsStatus()}})
	return append(out, &downexv1.Event{Kind: &downexv1.Event_Polite{Polite: m.politeStatusLocked()}})
}

// politeStatusLocked is the simulated Polite mode's status: the fixture's, else WATCHING with a
// smooth line while the setting is on, OFF otherwise.
func (m *Mock) politeStatusLocked() *downexv1.PoliteStatus {
	if m.politeFixed != nil {
		return proto.CloneOf(m.politeFixed)
	}
	if m.settings.GetPoliteMode() {
		return &downexv1.PoliteStatus{State: downexv1.PoliteState_POLITE_STATE_WATCHING, DelayMs: 3, BaseMs: 18, Since: stamp(m.startedAt)}
	}
	return &downexv1.PoliteStatus{State: downexv1.PoliteState_POLITE_STATE_OFF, DelayMs: -1, BaseMs: -1, Since: stamp(m.startedAt)}
}

// OnConfirmRequest subscribes to the daemon's requests to show a confirmation dialog.
func (m *Mock) OnConfirmRequest(f func(id int64)) (cancel func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.nextConfirm
	m.nextConfirm++
	m.confirmSubs[id] = f
	return func() { m.mu.Lock(); delete(m.confirmSubs, id); m.mu.Unlock() }
}

func (m *Mock) confirmListeners() []func(int64) {
	out := make([]func(int64), 0, len(m.confirmSubs))
	for _, f := range m.confirmSubs {
		out = append(out, f)
	}
	return out
}

// emitLocked collects the batch of everything that changed; the caller publishes it after
// unlocking.
func (m *Mock) emitLocked() (state.Batch, []func(state.Batch)) {
	var events state.Batch
	for _, id := range m.changed {
		if s := m.sims[id]; s != nil {
			events = append(events, &downexv1.Event{Kind: &downexv1.Event_Updated{Updated: s.d.clone()}})
		}
	}
	m.changed, m.changedS = nil, map[int64]bool{}
	for _, id := range m.removed {
		events = append(events, &downexv1.Event{Kind: &downexv1.Event_Removed{Removed: id}})
	}
	m.removed = nil
	for _, id := range m.changedQueues {
		if q := m.queues[id]; q != nil {
			events = append(events, &downexv1.Event{Kind: &downexv1.Event_QueueUpdated{QueueUpdated: q.clone()}})
		}
	}
	m.changedQueues, m.changedQS = nil, map[int64]bool{}
	for _, id := range m.removedQueues {
		events = append(events, &downexv1.Event{Kind: &downexv1.Event_QueueRemoved{QueueRemoved: id}})
	}
	m.removedQueues = nil
	for _, p := range m.postEvents {
		events = append(events, &downexv1.Event{Kind: &downexv1.Event_PostAction{PostAction: p}})
	}
	m.postEvents = nil
	if m.toolsChanged {
		events = append(events, &downexv1.Event{Kind: &downexv1.Event_Tools{Tools: m.toolsStatus()}})
	}
	m.toolsChanged = false
	if m.politeChanged {
		events = append(events, &downexv1.Event{Kind: &downexv1.Event_Polite{Polite: m.politeStatusLocked()}})
	}
	m.politeChanged = false
	if len(events) == 0 {
		return nil, nil
	}
	subs := make([]func(state.Batch), 0, len(m.subOrder))
	for _, id := range m.subOrder {
		subs = append(subs, m.subs[id])
	}
	return events, subs
}

// Tick advances the simulation by ms (0: one tick) and emits one event batch.
func (m *Mock) Tick(ms float64) {
	if ms <= 0 {
		ms = m.tickMs
	}
	m.mu.Lock()
	m.step(ms)
	batch, subs := m.emitLocked()
	m.mu.Unlock()
	for _, f := range subs {
		f(batch)
	}
}

// Window is what the page asked of its simulated window.
func (m *Mock) Window() WindowState {
	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.window
	w.Sizes = slices.Clone(w.Sizes)
	w.Opened = slices.Clone(w.Opened)
	w.Themes = slices.Clone(w.Themes)
	w.Folders = slices.Clone(w.Folders)
	return w
}

// respond waits the simulated latency.
func (m *Mock) respond(ctx context.Context) error {
	if m.latency <= 0 {
		return ctx.Err()
	}
	select {
	case <-time.After(m.latency):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
