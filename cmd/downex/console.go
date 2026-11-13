package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode/utf8"
)

// console writes human-readable status to stderr. On a terminal it keeps one
// progress line that is redrawn in place with a carriage return (no escape
// sequences, so it also works on consoles without ANSI support); other messages
// erase that line first so the two never interleave. It is safe for concurrent use.
type console struct {
	mu    sync.Mutex
	w     io.Writer
	live  bool // w is a terminal, so the progress line is drawn
	drawn int  // width of the progress line on screen, 0 if none
}

func newConsole(w io.Writer) *console {
	return &console{w: w, live: isTerminal(w)}
}

// isTerminal reports whether w is a character device such as a console.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// printf writes a message after erasing the progress line. String and error
// arguments are made printable (see printable): they may come from a server.
func (c *console) printf(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.eraseLocked()
	fmt.Fprintf(c.w, format, printableArgs(args)...)
}

// status redraws the progress line. It does nothing unless the console is a terminal.
func (c *console) status(line string) {
	if !c.live {
		return
	}
	line = printable(line)
	c.mu.Lock()
	defer c.mu.Unlock()
	width := utf8.RuneCountInString(line)
	fmt.Fprintf(c.w, "\r%s%s", line, strings.Repeat(" ", max(c.drawn-width, 0)))
	c.drawn = width
}

// keep ends the progress line so it stays visible above later output.
func (c *console) keep() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.drawn > 0 {
		fmt.Fprint(c.w, "\n")
		c.drawn = 0
	}
}

func (c *console) eraseLocked() {
	if c.drawn > 0 {
		fmt.Fprintf(c.w, "\r%s\r", strings.Repeat(" ", c.drawn))
		c.drawn = 0
	}
}
