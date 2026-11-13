package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// Defaults and bounds of Exec (docs/SITE-MEDIA-SPEC.md §4.7).
const (
	defaultMaxStdout = 1 << 20
	defaultMaxStderr = 64 << 10
	// DefaultMemoryLimit is the job memory limit of a yt-dlp run on Windows (2 GiB).
	DefaultMemoryLimit = 2 << 30
	// maxProcesses bounds the active processes of one child tree on Windows.
	maxProcesses = 32
	// exitGrace is how long descendants of an exited child may keep its output pipes
	// open before they are killed.
	exitGrace = 2 * time.Second
)

// ErrExec: the program could not be started.
var ErrExec = errors.New("cannot run the program")

// ExecSpec describes one child process.
type ExecSpec struct {
	// Path is the program's absolute path: no shell, no PATH search.
	Path string
	// Args are the arguments after the program name, each passed as one argument.
	Args []string
	// Env is the child's whole environment (nil = empty; never inherited): see Environment.
	Env []string
	// Dir is the working directory ("" = the daemon's).
	Dir string
	// Timeout kills the whole process tree when it elapses (0 = only ctx).
	Timeout time.Duration
	// MaxStdout bounds what is kept of stdout: more kills the tree and sets
	// StdoutTruncated (<= 0: 1 MiB).
	MaxStdout int
	// MaxStderr is how much of the end of stderr is kept (<= 0: 64 KiB).
	MaxStderr int
	// MemoryLimit bounds the committed memory of the whole tree on Windows, through its
	// job object (0 = no limit). Other systems do not limit memory.
	MemoryLimit int64
}

// ExecResult is what a child did.
type ExecResult struct {
	Stdout     []byte // at most MaxStdout bytes
	StderrTail []byte // the last MaxStderr bytes of stderr
	// ExitCode is the child's exit status; -1 when Exec killed it (timeout, output
	// limit, cancellation).
	ExitCode        int
	TimedOut        bool // the Timeout elapsed and the tree was killed
	StdoutTruncated bool // stdout exceeded MaxStdout and the tree was killed
}

// Exec runs the program of s and waits for it (docs/SITE-MEDIA-SPEC.md §4.7). The child
// gets an empty stdin, the environment s.Env only, and, on Windows, no console window
// (CREATE_NO_WINDOW: downexd is a GUI-subsystem program) and a job object that kills
// the whole tree together (at most 32 processes, the memory limit); elsewhere it leads
// its own process group, which is killed with SIGKILL. The tree is killed when the
// Timeout elapses, when stdout exceeds its limit and when ctx ends (then the error is
// ctx's); descendants still holding the output pipes shortly after the child exited
// are killed too. A child that ran returns a nil error whatever its exit code. A start
// refused because the program is still open for writing (ETXTBSY) is retried briefly.
func Exec(ctx context.Context, s ExecSpec) (ExecResult, error) {
	if !filepath.IsAbs(s.Path) {
		return ExecResult{}, fmt.Errorf("%w: %q is not an absolute path", ErrExec, s.Path)
	}
	if err := ctx.Err(); err != nil {
		return ExecResult{}, err
	}
	maxOut, maxErr := s.MaxStdout, s.MaxStderr
	if maxOut <= 0 {
		maxOut = defaultMaxStdout
	}
	if maxErr <= 0 {
		maxErr = defaultMaxStderr
	}
	env := s.Env
	if env == nil {
		env = []string{}
	}
	rOut, wOut, err := os.Pipe()
	if err != nil {
		return ExecResult{}, fmt.Errorf("%w: %v", ErrExec, err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		rOut.Close()
		wOut.Close()
		return ExecResult{}, fmt.Errorf("%w: %v", ErrExec, err)
	}
	cmd, tree, err := start(ctx, s, env, wOut, wErr)
	wOut.Close()
	wErr.Close()
	if err != nil {
		rOut.Close()
		rErr.Close()
		return ExecResult{}, fmt.Errorf("%w: %v", ErrExec, err)
	}

	stdout := &capture{max: maxOut}
	stderr := &capture{max: maxErr, tail: true}
	overflow := make(chan struct{})
	onOverflow := sync.OnceFunc(func() { close(overflow) })
	var readers sync.WaitGroup
	readers.Go(func() { drain(rOut, stdout, onOverflow) })
	readers.Go(func() { drain(rErr, stderr, nil) })
	readersDone := make(chan struct{})
	go func() {
		readers.Wait()
		close(readersDone)
	}()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	var timeout <-chan time.Time
	if s.Timeout > 0 {
		t := time.NewTimer(s.Timeout)
		defer t.Stop()
		timeout = t.C
	}
	var res ExecResult
	var waitErr, ctxErr error
	killed := true
	select {
	case waitErr = <-exited:
		killed = false
	case <-timeout:
		res.TimedOut = true
		tree.kill()
		waitErr = <-exited
	case <-overflow:
		res.StdoutTruncated = true
		tree.kill()
		waitErr = <-exited
	case <-ctx.Done():
		ctxErr = ctx.Err()
		tree.kill()
		waitErr = <-exited
	}
	// The child has exited; no descendant may outlive it.
	if killAtExit {
		tree.release(true)
	}
	select {
	case <-readersDone:
	case <-time.After(exitGrace):
		tree.release(true)
		select {
		case <-readersDone:
		case <-time.After(exitGrace):
			// A descendant that left the tree still holds a pipe: stop reading.
			rOut.Close()
			rErr.Close()
		}
	}
	tree.release(false)
	res.Stdout, res.StderrTail = stdout.bytes(), stderr.bytes()
	if stdout.truncated() {
		res.StdoutTruncated = true
	}
	switch {
	case killed:
		res.ExitCode = -1
	case waitErr == nil:
		res.ExitCode = 0
	default:
		var ee *exec.ExitError
		if !errors.As(waitErr, &ee) {
			return res, fmt.Errorf("%w: %v", ErrExec, waitErr)
		}
		res.ExitCode = ee.ExitCode()
	}
	return res, ctxErr
}

// textBusyRetries bounds the retries of a start refused with ETXTBSY (about 2 s of
// waits in all).
const textBusyRetries = 20

// start starts the child of s with its stdout and stderr on wOut and wErr. Linux refuses
// to run a file that any process has open for writing (ETXTBSY), and a child this process
// forks holds copies of all of its descriptors until its own exec (golang/go#22315): a
// copy written just before it runs (an install's smoke test, the first run after an
// update) is refused while a program that another goroutine started meanwhile has not
// reached its exec. That passes within milliseconds, so such a start is tried again
// after a growing wait, at most textBusyRetries times and only while ctx lasts.
func start(ctx context.Context, s ExecSpec, env []string, wOut, wErr *os.File) (*exec.Cmd, procTree, error) {
	for i := 1; ; i++ {
		// A Cmd starts only once: every try gets a new one.
		cmd := &exec.Cmd{Path: s.Path, Args: append([]string{s.Path}, s.Args...), Env: env, Dir: s.Dir, Stdout: wOut, Stderr: wErr}
		tree, err := startTree(cmd, s.MemoryLimit)
		if err == nil || !errors.Is(err, syscall.ETXTBSY) || i > textBusyRetries || ctx.Err() != nil {
			return cmd, tree, err
		}
		select {
		case <-ctx.Done():
			return cmd, tree, err
		case <-time.After(time.Duration(i) * 10 * time.Millisecond):
		}
	}
}

// procTree is a started child and its descendants.
type procTree interface {
	// kill kills the whole tree; the child has not been waited for yet.
	kill()
	// release frees the tree's resources once the child has exited, killing what remains
	// of it (always on Windows; elsewhere only when stragglers is true: something still
	// holds the output pipes, so the process group still has members). It may be called
	// more than once.
	release(stragglers bool)
}

// capture keeps the first (or, with tail, the last) max bytes written to it.
type capture struct {
	mu   sync.Mutex
	buf  []byte
	max  int
	tail bool
	over bool
}

// write records p and reports whether the limit was exceeded (never for a tail).
func (c *capture) write(p []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tail {
		c.buf = append(c.buf, p...)
		if len(c.buf) > 2*c.max {
			c.buf = append(c.buf[:0], c.buf[len(c.buf)-c.max:]...)
		}
		return false
	}
	if room := c.max - len(c.buf); room < len(p) {
		c.buf = append(c.buf, p[:max(room, 0)]...)
		c.over = true
		return true
	}
	c.buf = append(c.buf, p...)
	return false
}

func (c *capture) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.buf
	if c.tail && len(b) > c.max {
		b = b[len(b)-c.max:]
	}
	return append([]byte(nil), b...)
}

func (c *capture) truncated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.over
}

// drain reads r into c until EOF (or until r is closed), calling onOverflow when c's
// limit is exceeded; it keeps reading so that the child never blocks on a full pipe.
func drain(r *os.File, c *capture, onOverflow func()) {
	defer r.Close()
	b := make([]byte, 32<<10)
	for {
		n, err := r.Read(b)
		if n > 0 && c.write(b[:n]) && onOverflow != nil {
			onOverflow()
		}
		if err != nil {
			return
		}
	}
}
