package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"downex/internal/client"
	downexv1 "downex/internal/rpc/downex/v1"
	"downex/internal/rpc/downex/v1/downexv1connect"
)

// Queue bounds (proto Queue, Schedule).
const (
	maxQueueName       = 64   // characters
	maxQueueConcurrent = 16   // Queue.max_concurrent; 0 = Settings.max_concurrent_downloads
	maxRepeatMinutes   = 1440 // Schedule.repeat_minutes: at most once a day
	allWeekdays        = 0x7f // bit0 = Monday ... bit6 = Sunday
)

// runSub runs the subcommand of cmd named by args[0] (want lists them for errors). No
// arguments run def ("" = a usage error); -h prints the synopsis of every subcommand.
func (a *app) runSub(ctx context.Context, cmd, synopsis, about, want string, args []string, def string,
	subs map[string]func(context.Context, []string) error) error {
	if len(args) == 0 {
		if def == "" {
			return usageError{cmd, "expects a subcommand: " + want}
		}
		return subs[def](ctx, nil)
	}
	if isHelpFlag(args[0]) {
		fmt.Fprintf(a.usageOut, "Usage: downex %s\n\n%s\n\nRun 'downex %s <subcommand> -h' for the flags of a subcommand.\n",
			synopsis, about, cmd)
		return flag.ErrHelp
	}
	run, ok := subs[args[0]]
	if !ok {
		return usageError{cmd, fmt.Sprintf("unknown subcommand %q (want %s)", args[0], want)}
	}
	return run(ctx, args[1:])
}

const queueSynopsis = `queue ls [--json]
       downex queue create <name> [--max n] [--limit rate] [--post action] [--json]
       downex queue update <queue> [--name name] [--max n] [--limit rate] [--post action] [--json]
       downex queue rm <queue>
       downex queue start|stop <queue>
       downex queue move <id>... --to <queue> [--pos n]
       downex queue schedule <queue> [--start HH:MM] [--stop HH:MM|none] [--days list | --once YYYY-MM-DD]
                             [--repeat minutes] [--wake[=false]] [--off] [--json]`

const queueAbout = "Manage downexd's download queues. Every download belongs to one queue; the main queue\n" +
	"(id 1) always exists and runs, other queues start downloads only while they run (start/stop,\n" +
	"or their schedule). <queue> is a queue id or name (see \"downex queue ls\").\n\n" +
	"  ls        list the queues\n" +
	"  create    add a queue (stopped until started or scheduled); prints its id\n" +
	"  update    change a queue's name, limits or post-queue action\n" +
	"  rm        delete a queue; its downloads move to the end of the main queue\n" +
	"  start     run a queue now; stop pauses its running downloads (progress is saved)\n" +
	"  move      move downloads to a queue, at the end or at 1-based position --pos\n" +
	"  schedule  show or set when a queue starts and stops (local time)\n\n" +
	"--max is the number of downloads the queue runs at once (1-16, 0 = the global limit);\n" +
	"--post is what happens when the queue has finished: none, quit (stop downexd), sleep,\n" +
	"hibernate (Windows) or shutdown, after a 60 s countdown (\"downex cancel-post <queue>\")."

func (a *app) queue(ctx context.Context, args []string) error {
	return a.runSub(ctx, "queue", queueSynopsis, queueAbout,
		"ls, create, update, rm, start, stop, move or schedule", args, "ls", map[string]func(context.Context, []string) error{
			"ls": a.queueLs, "list": a.queueLs, "create": a.queueCreate, "update": a.queueUpdate,
			"rm": a.queueRm, "remove": a.queueRm, "start": a.queueStart, "stop": a.queueStop,
			"move": a.queueMove, "schedule": a.queueSchedule,
		})
}

// queueSession is a connection to the daemon's queue service.
type queueSession struct {
	c  *client.Client
	qc downexv1connect.QueueServiceClient
}

func (a *app) connectQueues(ctx context.Context, t *daemonTarget) (*queueSession, error) {
	c, err := a.connect(ctx, t)
	if err != nil {
		return nil, err
	}
	return &queueSession{c: c, qc: a.queueClient(c)}, nil
}

// list returns the daemon's queues.
func (s *queueSession) list(ctx context.Context) ([]*downexv1.Queue, error) {
	cctx, cancel := rpcContext(ctx)
	defer cancel()
	resp, err := s.qc.ListQueues(cctx, connect.NewRequest(&downexv1.ListQueuesRequest{}))
	if err != nil {
		return nil, rpcError("queues", err)
	}
	return resp.Msg.GetQueues(), nil
}

// find returns the queue arg names (an id or a name).
func (s *queueSession) find(ctx context.Context, arg string) (*downexv1.Queue, error) {
	qs, err := s.list(ctx)
	if err != nil {
		return nil, err
	}
	return findQueue(qs, arg)
}

// findQueue returns the queue arg names: a queue id; else a name, exact or (when only
// one matches) ignoring case; else "main" for the main queue.
func findQueue(qs []*downexv1.Queue, arg string) (*downexv1.Queue, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return nil, errors.New("the queue must not be empty")
	}
	if id, err := strconv.ParseInt(arg, 10, 64); err == nil {
		for _, q := range qs {
			if q.GetId() == id {
				return q, nil
			}
		}
	}
	for _, q := range qs {
		if q.GetName() == arg {
			return q, nil
		}
	}
	var folded []*downexv1.Queue
	for _, q := range qs {
		if strings.EqualFold(q.GetName(), arg) {
			folded = append(folded, q)
		}
	}
	switch {
	case len(folded) == 1:
		return folded[0], nil
	case len(folded) > 1:
		return nil, fmt.Errorf("queue name %q is ambiguous; use the queue's id (see \"downex queue ls\")", arg)
	}
	if strings.EqualFold(arg, "main") {
		for _, q := range qs {
			if q.GetIsMain() {
				return q, nil
			}
		}
	}
	return nil, fmt.Errorf("no queue %q (see \"downex queue ls\")", arg)
}

// queueArg returns the single <queue> argument of cmd.
func queueArg(cmd string, pos []string) (string, error) {
	if len(pos) != 1 {
		return "", usageError{cmd, "expects exactly one queue (an id or a name)"}
	}
	return pos[0], nil
}

func (a *app) queueLs(ctx context.Context, args []string) error {
	fs := a.flagSet("queue ls", "queue ls [--json]", "List downexd's queues with their state, limits, schedule and post-queue action.")
	t := a.daemonFlags(fs)
	asJSON := fs.Bool("json", false, "print a JSON array")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	if len(pos) != 0 {
		return usageError{"queue", "ls takes no arguments"}
	}
	s, err := a.connectQueues(ctx, t)
	if err != nil {
		return err
	}
	qs, err := s.list(ctx)
	if err != nil {
		return err
	}
	views := make([]queueView, 0, len(qs))
	for _, q := range qs {
		views = append(views, newQueueView(q))
	}
	if *asJSON {
		return writeJSON(a.stdout, views)
	}
	return writeQueues(a.stdout, views)
}

// queueSettings are the flags shared by create and update.
type queueSettings struct {
	name     string
	max      int
	limitArg string
	post     string
}

func (o *queueSettings) register(fs *flag.FlagSet, withName bool) {
	if withName {
		fs.StringVar(&o.name, "name", "", "rename the queue to `name`")
	}
	fs.IntVar(&o.max, "max", 0, fmt.Sprintf("run at most `n` of the queue's downloads at once (1-%d; 0 = the global limit)", maxQueueConcurrent))
	fs.StringVar(&o.limitArg, "limit", "", "limit the queue's downloads together to `rate` bytes per second, e.g. 2M = 2 MB/s (0 = unlimited)")
	fs.StringVar(&o.post, "post", "", "`action` when the queue has finished: none, quit, sleep, hibernate or shutdown")
}

// apply validates the flags given and sets them on q.
func (o *queueSettings) apply(cmd string, set map[string]bool, q *downexv1.Queue) error {
	if set["name"] {
		name, err := checkQueueName(o.name)
		if err != nil {
			return usageError{cmd, "--name: " + err.Error()}
		}
		q.Name = name
	}
	if set["max"] {
		if o.max < 0 || o.max > maxQueueConcurrent {
			return usageError{cmd, fmt.Sprintf("--max must be between 1 and %d (or 0 for the global limit), got %d", maxQueueConcurrent, o.max)}
		}
		q.MaxConcurrent = int32(o.max)
	}
	if set["limit"] {
		limit, err := parseRate(o.limitArg)
		if err != nil {
			return usageError{cmd, "--limit: " + err.Error()}
		}
		q.SpeedLimit = limit
	}
	if set["post"] {
		act, err := parsePostAction(o.post)
		if err != nil {
			return usageError{cmd, "--post: " + err.Error()}
		}
		q.PostAction = act
	}
	return nil
}

// checkQueueName returns name without surrounding spaces, or why it cannot be a queue name.
func checkQueueName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", errors.New("a queue name must not be empty")
	case utf8.RuneCountInString(name) > maxQueueName:
		return "", fmt.Errorf("a queue name has at most %d characters", maxQueueName)
	case strings.IndexFunc(name, needsEscape) >= 0 || !utf8.ValidString(name):
		return "", errors.New("a queue name must not contain control characters")
	}
	return name, nil
}

func (a *app) queueCreate(ctx context.Context, args []string) error {
	fs := a.flagSet("queue create", "queue create <name> [flags]",
		"Add a queue and print its id. It is stopped until \"downex queue start\" or its schedule\n"+
			"runs it; add downloads to it with \"downex add --queue <name>\".")
	t := a.daemonFlags(fs)
	var o queueSettings
	o.register(fs, false)
	asJSON := fs.Bool("json", false, "print the new queue as JSON instead of its id")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	if len(pos) != 1 {
		return usageError{"queue", "create expects exactly one queue name"}
	}
	name, err := checkQueueName(pos[0])
	if err != nil {
		return usageError{"queue", err.Error()}
	}
	q := &downexv1.Queue{Name: name}
	if err := o.apply("queue", visited(fs), q); err != nil {
		return err
	}
	s, err := a.connectQueues(ctx, t)
	if err != nil {
		return err
	}
	cctx, cancel := rpcContext(ctx)
	defer cancel()
	resp, err := s.qc.CreateQueue(cctx, connect.NewRequest(&downexv1.CreateQueueRequest{Queue: q}))
	if err != nil {
		return rpcError("queue create", err)
	}
	q = resp.Msg.GetQueue()
	if *asJSON {
		return writeJSON(a.stdout, newQueueView(q))
	}
	fmt.Fprintln(a.stdout, q.GetId())
	a.con.printf("Created queue %d (%s): %s.\n", q.GetId(), q.GetName(), describeQueue(q))
	if !q.GetRunning() {
		a.con.printf("Start it with: %s\n", commandLine(t, "queue", "start", idArg(q.GetId())))
	}
	return nil
}

func (a *app) queueUpdate(ctx context.Context, args []string) error {
	fs := a.flagSet("queue update", "queue update <queue> [flags]",
		"Change a queue's name, limits or post-queue action; everything else is kept.")
	t := a.daemonFlags(fs)
	var o queueSettings
	o.register(fs, true)
	asJSON := fs.Bool("json", false, "print the queue as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	arg, err := queueArg("queue", pos)
	if err != nil {
		return err
	}
	set := visited(fs)
	if !set["name"] && !set["max"] && !set["limit"] && !set["post"] {
		return usageError{"queue", "update needs --name, --max, --limit or --post"}
	}
	// Validate before contacting the daemon.
	if err := o.apply("queue", set, &downexv1.Queue{}); err != nil {
		return err
	}
	s, err := a.connectQueues(ctx, t)
	if err != nil {
		return err
	}
	q, err := s.find(ctx, arg)
	if err != nil {
		return err
	}
	// UpdateQueue replaces name, limits, schedule and post action: q, as read, carries
	// them all, and only the flags given change it.
	q = proto.Clone(q).(*downexv1.Queue)
	_ = o.apply("queue", set, q)
	cctx, cancel := rpcContext(ctx)
	defer cancel()
	resp, err := s.qc.UpdateQueue(cctx, connect.NewRequest(&downexv1.UpdateQueueRequest{Queue: q}))
	if err != nil {
		return rpcError("queue update", err)
	}
	q = resp.Msg.GetQueue()
	if *asJSON {
		return writeJSON(a.stdout, newQueueView(q))
	}
	fmt.Fprintf(a.stdout, "Updated queue %d (%s): %s.\n", q.GetId(), printable(q.GetName()), describeQueue(q))
	return nil
}

func (a *app) queueRm(ctx context.Context, args []string) error {
	fs := a.flagSet("queue rm", "queue rm <queue>",
		"Delete a queue. Its downloads are kept: they move to the end of the main queue.")
	t := a.daemonFlags(fs)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	arg, err := queueArg("queue", pos)
	if err != nil {
		return err
	}
	s, err := a.connectQueues(ctx, t)
	if err != nil {
		return err
	}
	q, err := s.find(ctx, arg)
	if err != nil {
		return err
	}
	if q.GetIsMain() {
		return fmt.Errorf("queue %d (%s) is the main queue, which cannot be deleted", q.GetId(), q.GetName())
	}
	cctx, cancel := rpcContext(ctx)
	defer cancel()
	if _, err := s.qc.DeleteQueue(cctx, connect.NewRequest(&downexv1.DeleteQueueRequest{Id: q.GetId()})); err != nil {
		return rpcError("queue rm", err)
	}
	fmt.Fprintf(a.stdout, "Removed queue %d (%s)", q.GetId(), printable(q.GetName()))
	if n := len(q.GetDownloadIds()); n > 0 {
		fmt.Fprintf(a.stdout, "; its %s moved to the main queue", plural(n, "download", "downloads"))
	}
	fmt.Fprintln(a.stdout, ".")
	return nil
}

func (a *app) queueStart(ctx context.Context, args []string) error {
	return a.queueRunState(ctx, args, true)
}

func (a *app) queueStop(ctx context.Context, args []string) error {
	return a.queueRunState(ctx, args, false)
}

// queueRunState starts (run) or stops a queue.
func (a *app) queueRunState(ctx context.Context, args []string, run bool) error {
	sub, about := "start", "Run a queue now: its queued downloads start in order, as many at once as its limit allows."
	if !run {
		sub, about = "stop", "Stop a queue: its running downloads are paused (their progress is saved) and the rest\n"+
			"stay queued until the queue runs again."
	}
	fs := a.flagSet("queue "+sub, "queue "+sub+" <queue>", about)
	t := a.daemonFlags(fs)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	arg, err := queueArg("queue", pos)
	if err != nil {
		return err
	}
	s, err := a.connectQueues(ctx, t)
	if err != nil {
		return err
	}
	q, err := s.find(ctx, arg)
	if err != nil {
		return err
	}
	cctx, cancel := rpcContext(ctx)
	defer cancel()
	if run {
		resp, err := s.qc.StartQueue(cctx, connect.NewRequest(&downexv1.StartQueueRequest{Id: q.GetId()}))
		if err != nil {
			return rpcError("queue start", err)
		}
		q = resp.Msg.GetQueue()
		fmt.Fprintf(a.stdout, "Started queue %d (%s) with %s.\n", q.GetId(), printable(q.GetName()),
			plural(len(q.GetDownloadIds()), "download", "downloads"))
		return nil
	}
	resp, err := s.qc.StopQueue(cctx, connect.NewRequest(&downexv1.StopQueueRequest{Id: q.GetId()}))
	if err != nil {
		return rpcError("queue stop", err)
	}
	q = resp.Msg.GetQueue()
	fmt.Fprintf(a.stdout, "Stopped queue %d (%s); its running downloads are paused.\n", q.GetId(), printable(q.GetName()))
	if q.GetIsMain() {
		fmt.Fprintf(a.stdout, "Run it again with: %s\n", commandLine(t, "queue", "start", idArg(q.GetId())))
	}
	return nil
}

func (a *app) queueMove(ctx context.Context, args []string) error {
	fs := a.flagSet("queue move", "queue move <id>... --to <queue> [--pos n]",
		"Move downloads (by download id, see \"downex ls\") to a queue: at its end, or with --pos\n"+
			"at 1-based position n of its order (1 = first), keeping the order given.")
	t := a.daemonFlags(fs)
	to := fs.String("to", "", "the target `queue` (id or name)")
	posArg := fs.Int("pos", 0, "insert at 1-based position `n` of the queue (default: at the end)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	if len(pos) == 0 {
		return usageError{"queue", "move expects one or more download ids"}
	}
	set := visited(fs)
	if !set["to"] {
		return usageError{"queue", "move needs --to <queue>"}
	}
	if set["pos"] && *posArg < 1 {
		return usageError{"queue", fmt.Sprintf("--pos must be 1 or more, got %d", *posArg)}
	}
	ids := make([]int64, 0, len(pos))
	for _, p := range pos {
		id, err := parseID("queue", []string{p})
		if err != nil {
			return err
		}
		if !slices.Contains(ids, int64(id)) {
			ids = append(ids, int64(id))
		}
	}
	position := int32(-1) // the end
	if set["pos"] {
		position = int32(*posArg - 1)
	}
	s, err := a.connectQueues(ctx, t)
	if err != nil {
		return err
	}
	q, err := s.find(ctx, *to)
	if err != nil {
		return err
	}
	cctx, cancel := rpcContext(ctx)
	defer cancel()
	resp, err := s.qc.MoveDownloads(cctx, connect.NewRequest(&downexv1.MoveDownloadsRequest{
		Ids: ids, QueueId: q.GetId(), Position: position,
	}))
	if err != nil {
		if connect.CodeOf(err) == connect.CodeNotFound {
			return &daemonError{msg: "queue move: " + connectMessage(err) + " (see \"downex ls\")", err: err}
		}
		return rpcError("queue move", err)
	}
	q = resp.Msg.GetQueue()
	strs := make([]string, len(ids))
	for i, id := range ids {
		strs[i] = idArg(id)
	}
	where := "the end"
	if set["pos"] {
		where = "position " + strconv.Itoa(*posArg)
	}
	noun := "downloads"
	if len(ids) == 1 {
		noun = "download"
	}
	fmt.Fprintf(a.stdout, "Moved %s %s to queue %d (%s) at %s.\n", noun, strings.Join(strs, ", "), q.GetId(),
		printable(q.GetName()), where)
	return nil
}

func (a *app) queueSchedule(ctx context.Context, args []string) error {
	fs := a.flagSet("queue schedule", "queue schedule <queue> [flags]",
		"Show a queue's schedule, or set it: the queue starts at --start (local time) on the\n"+
			"--days given (default: every day) or once on the --once date, and stops at --stop\n"+
			"(which may be after midnight). --repeat n restarts its unfinished downloads every n\n"+
			"minutes inside that window. --wake wakes the computer from sleep for the start\n"+
			"(Windows). --off turns the schedule off; flags not given keep their values.\n"+
			"--days takes a comma-separated list: mon,tue,wed,thu,fri,sat,sun, ranges such as\n"+
			"mon-fri, daily, weekdays or weekend.")
	t := a.daemonFlags(fs)
	var (
		start, stop, days, once string
		repeat                  int
		wake, off               bool
	)
	fs.StringVar(&start, "start", "", "start at `HH:MM` (24-hour local time)")
	fs.StringVar(&stop, "stop", "", "stop at `HH:MM`, or none")
	fs.StringVar(&days, "days", "", "start on these `days`, e.g. mon-fri or sat,sun (default: daily)")
	fs.StringVar(&once, "once", "", "start once, on `YYYY-MM-DD`, instead of on weekdays")
	fs.IntVar(&repeat, "repeat", 0, fmt.Sprintf("restart unfinished downloads every `minutes` inside the window (0 = off, at most %d)", maxRepeatMinutes))
	fs.BoolVar(&wake, "wake", false, "wake the computer from sleep for the start (Windows; --wake=false turns it off)")
	fs.BoolVar(&off, "off", false, "turn the schedule off")
	asJSON := fs.Bool("json", false, "print the queue as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	arg, err := queueArg("queue", pos)
	if err != nil {
		return err
	}
	set := visited(fs)
	delete(set, "json")
	delete(set, "db")
	delete(set, "endpoint")
	edit, err := parseScheduleEdit(set, start, stop, days, once, repeat, wake, off)
	if err != nil {
		return err
	}

	s, err := a.connectQueues(ctx, t)
	if err != nil {
		return err
	}
	q, err := s.find(ctx, arg)
	if err != nil {
		return err
	}
	if edit != nil {
		q = proto.Clone(q).(*downexv1.Queue)
		sched := q.GetSchedule()
		if sched == nil {
			sched = &downexv1.Schedule{}
		}
		if err := edit(sched); err != nil {
			return err
		}
		q.Schedule = sched
		cctx, cancel := rpcContext(ctx)
		defer cancel()
		resp, err := s.qc.UpdateQueue(cctx, connect.NewRequest(&downexv1.UpdateQueueRequest{Queue: q}))
		if err != nil {
			return rpcError("queue schedule", err)
		}
		q = resp.Msg.GetQueue()
	}
	if *asJSON {
		return writeJSON(a.stdout, newQueueView(q))
	}
	fmt.Fprintf(a.stdout, "Queue %d (%s): schedule %s.\n", q.GetId(), printable(q.GetName()), describeSchedule(q.GetSchedule()))
	return nil
}

// parseScheduleEdit validates the schedule flags given (set) and returns the change they
// make to a queue's schedule, or nil when none were given (show the schedule).
func parseScheduleEdit(set map[string]bool, start, stop, days, once string, repeat int, wake, off bool) (func(*downexv1.Schedule) error, error) {
	if len(set) == 0 {
		return nil, nil
	}
	if off {
		if len(set) > 1 {
			return nil, usageError{"queue", "--off cannot be combined with other schedule flags"}
		}
		return func(s *downexv1.Schedule) error { s.Enabled = false; return nil }, nil
	}
	// --off=false turns a schedule that is off on again with its stored times.
	explicitOn := set["off"]
	var (
		startMin, stopMin int
		weekdays          int32
		onceDate          string
		err               error
	)
	if set["start"] {
		if startMin, err = parseClock(start); err != nil {
			return nil, usageError{"queue", "--start: " + err.Error()}
		}
	}
	stopNone := false
	if set["stop"] {
		switch strings.ToLower(strings.TrimSpace(stop)) {
		case "", "none", "off", "never":
			stopNone = true
		default:
			if stopMin, err = parseClock(stop); err != nil {
				return nil, usageError{"queue", "--stop: " + err.Error()}
			}
		}
	}
	if set["days"] && set["once"] {
		return nil, usageError{"queue", "--days and --once cannot be combined"}
	}
	if set["days"] {
		if weekdays, err = parseWeekdays(days); err != nil {
			return nil, usageError{"queue", "--days: " + err.Error()}
		}
	}
	if set["once"] {
		d, err := time.ParseInLocation(time.DateOnly, strings.TrimSpace(once), time.Local)
		if err != nil {
			return nil, usageError{"queue", fmt.Sprintf("--once: want a date YYYY-MM-DD, got %q", once)}
		}
		onceDate = d.Format(time.DateOnly)
	}
	if set["repeat"] && (repeat < 0 || repeat > maxRepeatMinutes) {
		return nil, usageError{"queue", fmt.Sprintf("--repeat must be between 0 and %d minutes, got %d", maxRepeatMinutes, repeat)}
	}
	return func(s *downexv1.Schedule) error {
		if !set["start"] && !explicitOn && !s.GetEnabled() {
			return usageError{"queue", "the schedule is off: give --start HH:MM to turn it on"}
		}
		if set["start"] {
			s.StartMinute = int32(startMin)
		}
		if set["stop"] {
			if stopNone {
				s.StopEnabled = false
			} else {
				s.StopEnabled, s.StopMinute = true, int32(stopMin)
			}
		}
		if set["days"] {
			s.Weekdays, s.OnceDate = weekdays, ""
		}
		if set["once"] {
			s.Weekdays, s.OnceDate = 0, onceDate
		}
		if s.GetWeekdays() == 0 && s.GetOnceDate() == "" {
			s.Weekdays = allWeekdays
		}
		if set["repeat"] {
			s.RepeatMinutes = int32(repeat)
		}
		if set["wake"] {
			s.WakeComputer = wake
		}
		if s.GetStopEnabled() && s.GetStopMinute() == s.GetStartMinute() {
			return usageError{"queue", "the stop time must differ from the start time"}
		}
		s.Enabled = true
		return nil
	}, nil
}

// parseClock parses "HH:MM" (24-hour) into minutes after midnight.
func parseClock(s string) (int, error) {
	digits := func(v string) bool {
		return v != "" && strings.Trim(v, "0123456789") == ""
	}
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	hh, herr := strconv.Atoi(h)
	mm, merr := strconv.Atoi(m)
	if !ok || !digits(h) || !digits(m) || herr != nil || merr != nil || len(m) != 2 || len(h) > 2 || hh > 23 || mm > 59 {
		return 0, fmt.Errorf("want a time HH:MM between 00:00 and 23:59, got %q", s)
	}
	return hh*60 + mm, nil
}

// formatClock formats minutes after midnight as "HH:MM".
func formatClock(m int32) string {
	return fmt.Sprintf("%02d:%02d", m/60, m%60)
}

// weekdayNames are the day names of Schedule.weekdays, bit 0 (Monday) first.
var weekdayNames = [7]string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// parseDay returns the bit index of a day name ("mon", "monday", ...).
func parseDay(s string) (int, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) < 3 {
		return 0, false
	}
	full := [7]string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}
	for i, d := range full {
		if strings.HasPrefix(d, s) {
			return i, true
		}
	}
	return 0, false
}

// parseWeekdays parses a comma-separated list of days, day ranges ("mon-fri", also
// across the week's end: "fri-mon") and the words daily, weekdays and weekend.
func parseWeekdays(s string) (int32, error) {
	var mask int32
	for part := range strings.SplitSeq(s, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		switch part {
		case "":
			continue
		case "daily", "all", "every", "everyday", "every-day":
			mask |= allWeekdays
			continue
		case "weekdays", "workdays":
			mask |= 0x1f
			continue
		case "weekend", "weekends":
			mask |= 0x60
			continue
		}
		from, to, isRange := strings.Cut(part, "-")
		a, ok := parseDay(from)
		if !ok {
			return 0, fmt.Errorf("unknown day %q (want mon, tue, wed, thu, fri, sat or sun)", from)
		}
		b := a
		if isRange {
			if b, ok = parseDay(to); !ok {
				return 0, fmt.Errorf("unknown day %q (want mon, tue, wed, thu, fri, sat or sun)", to)
			}
		}
		for i := a; ; i = (i + 1) % 7 {
			mask |= 1 << i
			if i == b {
				break
			}
		}
	}
	if mask == 0 {
		return 0, errors.New("no days given")
	}
	return mask, nil
}

// weekdayList names the days of mask in week order.
func weekdayList(mask int32) []string {
	var out []string
	for i, d := range weekdayNames {
		if mask&(1<<i) != 0 {
			out = append(out, d)
		}
	}
	return out
}

// describeDays summarises mask: "daily", "weekdays", "weekend" or "mon,wed".
func describeDays(mask int32) string {
	switch mask & allWeekdays {
	case allWeekdays:
		return "daily"
	case 0x1f:
		return "weekdays"
	case 0x60:
		return "weekend"
	}
	return strings.Join(weekdayList(mask), ",")
}

// parsePostAction parses a post-queue action name.
func parsePostAction(s string) (downexv1.PostAction, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "none", "off", "nothing":
		return downexv1.PostAction_POST_ACTION_UNSPECIFIED, nil
	case "quit", "exit":
		return downexv1.PostAction_POST_ACTION_QUIT_DOWNEX, nil
	case "sleep", "suspend":
		return downexv1.PostAction_POST_ACTION_SLEEP, nil
	case "hibernate":
		return downexv1.PostAction_POST_ACTION_HIBERNATE, nil
	case "shutdown", "shut-down", "poweroff":
		return downexv1.PostAction_POST_ACTION_SHUTDOWN, nil
	}
	return 0, fmt.Errorf("want none, quit, sleep, hibernate or shutdown, got %q", s)
}

// postActionName names a post-queue action as --post takes it.
func postActionName(a downexv1.PostAction) string {
	switch a {
	case downexv1.PostAction_POST_ACTION_UNSPECIFIED:
		return "none"
	case downexv1.PostAction_POST_ACTION_QUIT_DOWNEX:
		return "quit"
	case downexv1.PostAction_POST_ACTION_SLEEP:
		return "sleep"
	case downexv1.PostAction_POST_ACTION_HIBERNATE:
		return "hibernate"
	case downexv1.PostAction_POST_ACTION_SHUTDOWN:
		return "shutdown"
	}
	return fmt.Sprintf("action(%d)", int32(a))
}

// postActionText describes a post-queue action for people.
func postActionText(a downexv1.PostAction) string {
	switch a {
	case downexv1.PostAction_POST_ACTION_QUIT_DOWNEX:
		return "quit Downex"
	case downexv1.PostAction_POST_ACTION_SLEEP:
		return "put the computer to sleep"
	case downexv1.PostAction_POST_ACTION_HIBERNATE:
		return "hibernate the computer"
	case downexv1.PostAction_POST_ACTION_SHUTDOWN:
		return "shut down the computer"
	}
	return postActionName(a)
}

// queueView is the JSON form of a queue.
type queueView struct {
	ID            int64         `json:"id"`
	Name          string        `json:"name"`
	Main          bool          `json:"main"`
	Running       bool          `json:"running"`
	MaxConcurrent int           `json:"maxConcurrent"` // 0 = the global limit
	SpeedLimit    int64         `json:"speedLimit"`    // bytes per second, 0 = unlimited
	PostAction    string        `json:"postAction"`    // none, quit, sleep, hibernate or shutdown
	Schedule      *scheduleView `json:"schedule,omitempty"`
	DownloadIDs   []int64       `json:"downloadIds"` // in queue order
}

// scheduleView is the JSON form of a queue's schedule.
type scheduleView struct {
	Enabled bool     `json:"enabled"`
	Start   string   `json:"start"`          // HH:MM local time
	Stop    string   `json:"stop,omitempty"` // HH:MM, "" = no stop time
	Days    []string `json:"days,omitempty"` // mon..sun; empty with Once
	Once    string   `json:"once,omitempty"` // YYYY-MM-DD
	Repeat  int      `json:"repeatMinutes,omitempty"`
	Wake    bool     `json:"wakeComputer,omitempty"`
}

func newQueueView(q *downexv1.Queue) queueView {
	v := queueView{
		ID: q.GetId(), Name: q.GetName(), Main: q.GetIsMain(), Running: q.GetRunning(),
		MaxConcurrent: int(q.GetMaxConcurrent()), SpeedLimit: q.GetSpeedLimit(),
		PostAction: postActionName(q.GetPostAction()), DownloadIDs: q.GetDownloadIds(),
	}
	if v.DownloadIDs == nil {
		v.DownloadIDs = []int64{}
	}
	if s := q.GetSchedule(); s != nil {
		sv := &scheduleView{
			Enabled: s.GetEnabled(), Start: formatClock(s.GetStartMinute()), Days: weekdayList(s.GetWeekdays()),
			Once: s.GetOnceDate(), Repeat: int(s.GetRepeatMinutes()), Wake: s.GetWakeComputer(),
		}
		if s.GetStopEnabled() {
			sv.Stop = formatClock(s.GetStopMinute())
		}
		v.Schedule = sv
	}
	return v
}

// describeSchedule summarises a schedule: "off", or e.g. "weekdays 02:00-06:00,
// repeat every 30 min, wakes the computer".
func describeSchedule(s *downexv1.Schedule) string {
	if !s.GetEnabled() {
		return "off"
	}
	when := describeDays(s.GetWeekdays())
	if s.GetWeekdays() == 0 {
		when = "once " + s.GetOnceDate()
	}
	parts := []string{when + " " + formatClock(s.GetStartMinute())}
	if s.GetStopEnabled() {
		parts[0] += "-" + formatClock(s.GetStopMinute())
	}
	if n := s.GetRepeatMinutes(); n > 0 {
		parts = append(parts, fmt.Sprintf("repeat every %d min", n))
	}
	if s.GetWakeComputer() {
		parts = append(parts, "wakes the computer")
	}
	return strings.Join(parts, ", ")
}

// describeQueue summarises a queue's state and settings on one line.
func describeQueue(q *downexv1.Queue) string {
	state := "stopped"
	if q.GetRunning() {
		state = "running"
	}
	conc := "the global limit of downloads at once"
	if n := q.GetMaxConcurrent(); n > 0 {
		conc = fmt.Sprintf("up to %d at once", n)
	}
	parts := []string{state, conc, "speed limit " + formatRate(q.GetSpeedLimit())}
	if s := q.GetSchedule(); s.GetEnabled() {
		parts = append(parts, "schedule "+describeSchedule(s))
	}
	if act := q.GetPostAction(); act != downexv1.PostAction_POST_ACTION_UNSPECIFIED {
		parts = append(parts, "when finished: "+postActionText(act))
	}
	return strings.Join(parts, ", ")
}

// writeQueues prints queues as a table.
func writeQueues(w io.Writer, views []queueView) error {
	if len(views) == 0 {
		fmt.Fprintln(w, "No queues.")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tSTATE\tDOWNLOADS\tAT ONCE\tLIMIT\tSCHEDULE\tWHEN DONE")
	for _, v := range views {
		name := printable(v.Name)
		if v.Main {
			name += " (main)"
		}
		state := "stopped"
		if v.Running {
			state = "running"
		}
		conc := "default"
		if v.MaxConcurrent > 0 {
			conc = strconv.Itoa(v.MaxConcurrent)
		}
		sched := "-"
		if v.Schedule != nil && v.Schedule.Enabled {
			sched = describeScheduleView(v.Schedule)
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n", v.ID, name, state, len(v.DownloadIDs), conc,
			formatRate(v.SpeedLimit), sched, v.PostAction)
	}
	return tw.Flush()
}

// describeScheduleView is describeSchedule for the table: the window and its days.
func describeScheduleView(s *scheduleView) string {
	when := "once " + s.Once
	if len(s.Days) > 0 {
		var mask int32
		for _, d := range s.Days {
			if i := slices.Index(weekdayNames[:], d); i >= 0 {
				mask |= 1 << i
			}
		}
		when = describeDays(mask)
	}
	out := when + " " + s.Start
	if s.Stop != "" {
		out += "-" + s.Stop
	}
	if s.Repeat > 0 {
		out += fmt.Sprintf(" /%dm", s.Repeat)
	}
	if s.Wake {
		out += " wake"
	}
	return out
}

// plural formats n with the singular or plural noun: "1 download", "3 downloads".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// connectMessage returns the message of a Connect error (its text without the code).
func connectMessage(err error) string {
	var ce *connect.Error
	if errors.As(err, &ce) && ce.Message() != "" {
		return ce.Message()
	}
	return err.Error()
}

func (a *app) cancelPost(ctx context.Context, args []string) error {
	fs := a.flagSet("cancel-post", "cancel-post <queue>",
		"Cancel the countdown of a queue's post-queue action (sleep, hibernate, shutdown or quit),\n"+
			"which starts when the queue has finished its downloads. <queue> is an id or a name.")
	t := a.daemonFlags(fs)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return silentError{err}
	}
	if len(pos) != 1 {
		return usageError{"cancel-post", "expects exactly one queue (an id or a name)"}
	}
	s, err := a.connectQueues(ctx, t)
	if err != nil {
		return err
	}
	q, err := s.find(ctx, pos[0])
	if err != nil {
		return err
	}
	cctx, cancel := rpcContext(ctx)
	defer cancel()
	if _, err := s.c.Daemon.CancelPostAction(cctx, connect.NewRequest(&downexv1.CancelPostActionRequest{QueueId: q.GetId()})); err != nil {
		if connect.CodeOf(err) == connect.CodeFailedPrecondition || connect.CodeOf(err) == connect.CodeNotFound {
			return &daemonError{msg: fmt.Sprintf("cancel-post: no post-queue action of queue %d (%s) is counting down (%s)",
				q.GetId(), q.GetName(), connectMessage(err)), err: err}
		}
		return rpcError("cancel-post", err)
	}
	fmt.Fprintf(a.stdout, "Cancelled the post-queue action of queue %d (%s).\n", q.GetId(), printable(q.GetName()))
	return nil
}

// queueFlagUsage describes the --queue flag of add, media and confirm.
const queueFlagUsage = "put the download into `queue` (an id or a name, see \"downex queue ls\"; default: the main queue)"

// resolveQueueFlag returns the queue arg (an id or a name) names on c's daemon.
func (a *app) resolveQueueFlag(ctx context.Context, c *client.Client, arg string) (*downexv1.Queue, error) {
	s := &queueSession{c: c, qc: a.queueClient(c)}
	return s.find(ctx, arg)
}

// hintStoppedQueue tells that a download queued in q does not start before q runs.
func (a *app) hintStoppedQueue(t *daemonTarget, q *downexv1.Queue, d *downexv1.Download) {
	if q == nil || q.GetRunning() || d.GetState() != downexv1.DownloadState_DOWNLOAD_STATE_QUEUED {
		return
	}
	a.con.printf("Queue %d (%s) is not running: the download starts when it runs (%s).\n",
		q.GetId(), q.GetName(), commandLine(t, "queue", "start", idArg(q.GetId())))
}
