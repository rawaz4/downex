package main

import (
	"context"
	"log/slog"
	"slices"
	"strconv"
	"strings"
)

// consoleHandler is a slog.Handler that prints engine log records as short lines,
// "warning: message (key=value ...)", through the console so they never break the
// progress line.
type consoleHandler struct {
	con    *console
	level  slog.Level
	attrs  []slog.Attr // already qualified with their group prefix
	prefix string      // "group." prefix for later attributes
}

func newConsoleHandler(con *console, level slog.Level) *consoleHandler {
	return &consoleHandler{con: con, level: level}
}

func (h *consoleHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *consoleHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(levelLabel(r.Level))
	b.WriteString(": ")
	b.WriteString(r.Message)
	attrs := slices.Clone(h.attrs)
	r.Attrs(func(a slog.Attr) bool {
		attrs = appendAttr(attrs, h.prefix, a)
		return true
	})
	if len(attrs) > 0 {
		b.WriteString(" (")
		for i, a := range attrs {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(a.Key)
			b.WriteByte('=')
			b.WriteString(quoteValue(a.Value.String()))
		}
		b.WriteByte(')')
	}
	h.con.printf("%s\n", b.String())
	return nil
}

func (h *consoleHandler) WithAttrs(as []slog.Attr) slog.Handler {
	c := *h
	c.attrs = slices.Clone(h.attrs)
	for _, a := range as {
		c.attrs = appendAttr(c.attrs, h.prefix, a)
	}
	return &c
}

func (h *consoleHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := *h
	c.prefix = h.prefix + name + "."
	return &c
}

// appendAttr appends a, resolved and flattened, with its keys qualified by prefix.
func appendAttr(dst []slog.Attr, prefix string, a slog.Attr) []slog.Attr {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return dst
	}
	if a.Value.Kind() == slog.KindGroup {
		p := prefix
		if a.Key != "" {
			p += a.Key + "."
		}
		for _, g := range a.Value.Group() {
			dst = appendAttr(dst, p, g)
		}
		return dst
	}
	a.Key = prefix + a.Key
	return append(dst, a)
}

func levelLabel(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "error"
	case l >= slog.LevelWarn:
		return "warning"
	case l >= slog.LevelInfo:
		return "info"
	}
	return "debug"
}

// quoteValue quotes s when it is empty or contains spaces, quotes, '=' or control
// characters, so key=value pairs stay unambiguous.
func quoteValue(s string) string {
	if s == "" || strings.ContainsFunc(s, func(r rune) bool {
		return r <= ' ' || r == '"' || r == '=' || r == 0x7f
	}) {
		return strconv.Quote(s)
	}
	return s
}
