package state

import (
	"regexp"

	"connectrpc.com/connect"

	"downex/ui/host"
)

var codePrefix = regexp.MustCompile(`(?s)^([a-z_]+):\s*(.*)$`)

// codeNames maps the code names the window's texts start with to their codes.
var codeNames = func() map[string]connect.Code {
	m := map[string]connect.Code{}
	for c := connect.CodeCanceled; c <= connect.CodeUnauthenticated; c++ {
		m[c.String()] = c
	}
	return m
}()

// ErrorOf is today's toApiError: host.ErrorOf, and an error that is no Connect error but
// whose text starts with a code name ("unimplemented: …", as a plain error or a
// rejection text) takes that code and the rest as its message.
func ErrorOf(err error) *host.CallError {
	ce := host.ErrorOf(err)
	if ce == nil || ce.Code != connect.CodeUnknown {
		return ce
	}
	if m := codePrefix.FindStringSubmatch(ce.Message); m != nil {
		if code, ok := codeNames[m[1]]; ok {
			msg := m[2]
			if msg == "" {
				msg = ce.Message
			}
			return &host.CallError{Code: code, Message: msg}
		}
	}
	return ce
}

// Message is ErrorOf(err).Message: the text the window shows for a failed call.
func Message(err error) string {
	if ce := ErrorOf(err); ce != nil {
		return ce.Message
	}
	return ""
}

// codeOf is ErrorOf(err).Code (CodeUnknown for nil).
func codeOf(err error) connect.Code {
	if ce := ErrorOf(err); ce != nil {
		return ce.Code
	}
	return connect.CodeUnknown
}
