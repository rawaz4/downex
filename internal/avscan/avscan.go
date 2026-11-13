// Package avscan runs the user's antivirus scanner on a downloaded file
// (docs/M6-SPEC.md §3, av_scan_enabled / av_scan_command / av_scan_args). The scanner
// is an absolute executable path started directly, never through a shell; in its
// arguments every [File] (case-insensitive) is replaced by the file's path:
//
//   - Windows: the arguments are a command line as the user wrote it, with [File]
//     (or "[File]") replaced by the path in double quotes; the scanner parses its
//     command line itself, as every Windows program does. No console window opens.
//   - Elsewhere: the arguments are split into words like a POSIX shell would (blanks
//     separate; '…', "…" and \ quote) without any expansion, and [File] inside a word
//     is replaced by the path, which stays within that one argument.
//
// The scan is bounded by a timeout (DefaultTimeout, 10 minutes); its result is the
// exit code: 0 = nothing found, anything else = the scanner reported a problem.
package avscan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// Placeholder is replaced by the scanned file's path in the arguments.
	Placeholder = "[File]"
	// DefaultArgs are the arguments of a new installation: just the file.
	DefaultArgs = Placeholder
	// DefaultTimeout bounds one scan.
	DefaultTimeout = 10 * time.Minute
	// MaxArgsBytes bounds the argument string.
	MaxArgsBytes = 4096
	// MaxCommandBytes bounds the command path.
	MaxCommandBytes = 4096
	// waitDelay bounds how long the scanner's output pipes may stay open after it
	// ended or was stopped (a child process of the scanner may hold them).
	waitDelay = 5 * time.Second
	// maxOutput bounds the scanner output that is kept for the log.
	maxOutput = 4096
)

// ErrTimeout reports a scan that did not finish within the timeout; the scanner was
// stopped.
var ErrTimeout = errors.New("avscan: the scanner did not finish in time")

// Result is the outcome of a scan that ran to its end.
type Result struct {
	// ExitCode is the scanner's exit code; -1 when it was ended by a signal.
	ExitCode int
	// Output is the start of what the scanner wrote (standard output and error),
	// for the log.
	Output   string
	Duration time.Duration
}

// Options tune Run.
type Options struct {
	Timeout time.Duration // 0 = DefaultTimeout
}

// CheckCommand validates a scanner path: absolute, existing and a regular file
// (symbolic links are followed); on Windows a program (.exe or .com), as batch files
// would run through the command interpreter.
func CheckCommand(command string) error {
	switch {
	case command == "":
		return errors.New("the scanner command is empty")
	case len(command) > MaxCommandBytes:
		return fmt.Errorf("the scanner command is longer than %d bytes", MaxCommandBytes)
	case strings.ContainsRune(command, 0) || !utf8.ValidString(command):
		return errors.New("the scanner command is not a valid path")
	case !filepath.IsAbs(command):
		return errors.New("the scanner command must be an absolute path")
	}
	fi, err := os.Stat(command)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("the scanner command does not exist")
		}
		return fmt.Errorf("the scanner command cannot be read: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return errors.New("the scanner command is not a file")
	}
	return checkCommandPlatform(command)
}

// CheckArgs validates an argument string (see the package documentation for how it
// is split).
func CheckArgs(args string) error {
	switch {
	case len(args) > MaxArgsBytes:
		return fmt.Errorf("the scanner arguments are longer than %d bytes", MaxArgsBytes)
	case strings.ContainsRune(args, 0) || !utf8.ValidString(args):
		return errors.New("the scanner arguments hold invalid characters")
	}
	return checkArgsPlatform(args)
}

// Run scans file (an absolute path) with command and args. A scan that ran to its end
// returns its Result, whatever the exit code; an error means the scan did not happen
// or did not finish: the scanner could not be started, it timed out (ErrTimeout) or
// ctx ended (ctx.Err()); the scanner is stopped then.
func Run(ctx context.Context, command, args, file string, opts Options) (Result, error) {
	if err := CheckCommand(command); err != nil {
		return Result{}, fmt.Errorf("avscan: %w", err)
	}
	if err := CheckArgs(args); err != nil {
		return Result{}, fmt.Errorf("avscan: %w", err)
	}
	if !filepath.IsAbs(file) || strings.ContainsRune(file, 0) {
		return Result{}, errors.New("avscan: the file path must be absolute")
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, command)
	if err := setCommandLine(cmd, args, file); err != nil {
		return Result{}, fmt.Errorf("avscan: %w", err)
	}
	out := &limitedBuffer{limit: maxOutput}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = waitDelay
	start := time.Now()
	err := cmd.Run()
	res := Result{Output: out.String(), Duration: time.Since(start)}
	if err == nil {
		return res, nil
	}
	switch {
	case ctx.Err() != nil:
		return res, ctx.Err()
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return res, ErrTimeout
	}
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		res.ExitCode = exit.ExitCode()
	case errors.Is(err, exec.ErrWaitDelay):
		res.ExitCode = cmd.ProcessState.ExitCode()
	default:
		return res, fmt.Errorf("avscan: run the scanner: %w", err)
	}
	return res, nil
}

// replacePlaceholder replaces every [File] of s (case-insensitively) with repl.
func replacePlaceholder(s, repl string) string {
	var b strings.Builder
	for {
		i := indexFold(s, Placeholder)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		b.WriteString(repl)
		s = s[i+len(Placeholder):]
	}
}

// indexFold is strings.Index ignoring ASCII case.
func indexFold(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if strings.EqualFold(s[i:i+len(sub)], sub) {
			return i
		}
	}
	return -1
}

// splitArgs splits args into words like a POSIX shell without expansions: blanks
// separate words; inside '…' everything is literal; inside "…" a backslash escapes
// only " \ $ and `; elsewhere a backslash escapes the next character. An unterminated
// quote or a trailing backslash is an error.
func splitArgs(args string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(args); i++ {
		c := args[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		case c == '\'':
			j := strings.IndexByte(args[i+1:], '\'')
			if j < 0 {
				return nil, errors.New("the scanner arguments have an unterminated ' quote")
			}
			cur.WriteString(args[i+1 : i+1+j])
			i += j + 1
			inWord = true
		case c == '"':
			i++
			for ; i < len(args) && args[i] != '"'; i++ {
				if args[i] == '\\' && i+1 < len(args) && strings.IndexByte("\"\\$`", args[i+1]) >= 0 {
					i++
				}
				cur.WriteByte(args[i])
			}
			if i >= len(args) {
				return nil, errors.New("the scanner arguments have an unterminated \" quote")
			}
			inWord = true
		case c == '\\':
			if i+1 >= len(args) {
				return nil, errors.New("the scanner arguments end with a backslash")
			}
			i++
			cur.WriteByte(args[i])
			inWord = true
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}

// limitedBuffer keeps the first limit bytes written to it.
type limitedBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.limit - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// String returns the kept output with invalid UTF-8 and control characters other
// than line breaks and tabs replaced.
func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r == '\r':
			return -1
		case r < ' ' || r == 0x7f || r == utf8.RuneError:
			return '?'
		}
		return r
	}, strings.TrimSpace(b.buf.String()))
}
