package host

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
)

// How long the window waits for downexd (today's bindings: ui/app.go, app_m6.go,
// app_site.go). The window gives each call a context with its bound, which also ends
// when the window closes.
const (
	// CallTimeout bounds one call's daemon work, including the wait for the start of
	// downexd when the window opens (≤ 3 s).
	CallTimeout = 30 * time.Second
	// CheckTimeout bounds CheckForUpdate: downexd fetches the manifest (≤ 1 MiB) and its
	// signature from the network.
	CheckTimeout = 60 * time.Second
	// InstallTimeout bounds InstallUpdate: downexd downloads the whole installer first.
	InstallTimeout = 15 * time.Minute
	// ToolsInstallTimeout bounds InstallTools: downexd answers when the install ends, or
	// with the progress so far at this deadline while the install goes on.
	ToolsInstallTimeout = 15 * time.Minute
	// ShutdownWait bounds how long closing the window waits for the events bridge.
	ShutdownWait = 3 * time.Second
)

// CallError is a failed call as the window reports it: its Connect code and message,
// written "<code>: <message>" (the text today's window shows, connect.Error's format).
type CallError struct {
	Code    connect.Code
	Message string
}

func (e *CallError) Error() string { return e.Code.String() + ": " + e.Message }

// Err is e as an error: nil when e is nil (a nil *CallError in an error is not nil).
func (e *CallError) Err() error {
	if e == nil {
		return nil
	}
	return e
}

// ErrorOf maps err to a CallError; nil stays nil (return it as an error with Err). Connect
// errors keep their code and message; a context that ended maps to canceled or
// deadline_exceeded; anything else is unknown. An empty message reads "no details".
func ErrorOf(err error) *CallError {
	if err == nil {
		return nil
	}
	var ce *CallError
	if errors.As(err, &ce) {
		return ce
	}
	code := connect.CodeOf(err)
	msg := err.Error()
	var cerr *connect.Error
	if errors.As(err, &cerr) && cerr == err {
		msg = cerr.Message()
	}
	if code == connect.CodeUnknown {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			code, msg = connect.CodeDeadlineExceeded, "downexd did not answer in time"
		case errors.Is(err, context.Canceled):
			code, msg = connect.CodeCanceled, "the window is closing"
		}
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		msg = "no details"
	}
	return &CallError{Code: code, Message: msg}
}

// ErrNoWindow is the error of a call that needs the window (a dialog) before it exists.
func ErrNoWindow() error {
	return connect.NewError(connect.CodeUnavailable, errors.New("the window is not ready"))
}
