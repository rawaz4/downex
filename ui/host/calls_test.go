package host

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"testing"

	"connectrpc.com/connect"
)

// windowError is the "<code>: <message>" format today's frontend parses into ApiError.code.
var windowError = regexp.MustCompile(`^([a-z_]+): (.+)$`)

// TestErrorsAreConnectText: every failure reaches the window as its Connect code and
// message (today's TestErrorsAreConnectText).
func TestErrorsAreConnectText(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{connect.NewError(connect.CodeNotFound, errors.New("no such download")), "not_found: no such download"},
		{connect.NewError(connect.CodeFailedPrecondition, errors.New("download 7 is completed")), "failed_precondition: download 7 is completed"},
		{connect.NewError(connect.CodeUnavailable, fmt.Errorf("connect to downexd: %w", errors.New("pipe busy"))), "unavailable: connect to downexd: pipe busy"},
		{fmt.Errorf("wrapped: %w", connect.NewError(connect.CodeInvalidArgument, errors.New("bad url"))), "invalid_argument: wrapped: invalid_argument: bad url"},
		{context.DeadlineExceeded, "deadline_exceeded: downexd did not answer in time"},
		{fmt.Errorf("call: %w", context.Canceled), "canceled: the window is closing"},
		{errors.New("something else"), "unknown: something else"},
		{connect.NewError(connect.CodeInternal, nil), "internal: no details"},
		{connect.NewError(connect.CodeInternal, errors.New("  padded  ")), "internal: padded"},
	} {
		got := ErrorOf(tc.err)
		if got == nil || got.Error() != tc.want {
			t.Errorf("ErrorOf(%v) = %v, want %q", tc.err, got, tc.want)
			continue
		}
		if m := windowError.FindStringSubmatch(got.Error()); m == nil || m[1] != got.Code.String() || m[2] != got.Message {
			t.Errorf("%q is not \"<code>: <message>\"", got.Error())
		}
		// Mapping twice changes nothing.
		if again := ErrorOf(got); again != got {
			t.Errorf("ErrorOf(ErrorOf(%v)) = %v", tc.err, again)
		}
	}
	if ErrorOf(nil) != nil {
		t.Error("ErrorOf(nil) is not nil")
	}
	if err := ErrorOf(nil).Err(); err != nil {
		t.Errorf("ErrorOf(nil).Err() = %v, want a nil error", err)
	}
	if err := ErrorOf(errors.New("x")).Err(); err == nil || err.Error() != "unknown: x" {
		t.Errorf("Err() = %v", err)
	}
	if got := ErrNoWindow(); errText(got) != "unavailable: the window is not ready" {
		t.Errorf("ErrNoWindow = %v", got)
	}
}

// The bounds today's bindings give each kind of call.
func TestCallTimeouts(t *testing.T) {
	if CallTimeout.Seconds() != 30 || CheckTimeout.Seconds() != 60 || InstallTimeout.Minutes() != 15 ||
		ToolsInstallTimeout.Minutes() != 15 || ShutdownWait.Seconds() != 3 {
		t.Errorf("timeouts %v %v %v %v %v", CallTimeout, CheckTimeout, InstallTimeout, ToolsInstallTimeout, ShutdownWait)
	}
	s, _ := newTestSession(t, newFakeDaemon())
	ctx, cancel := s.CallContext()
	defer cancel()
	if _, ok := ctx.Deadline(); !ok {
		t.Error("a call's context has no deadline")
	}
	s.Close()
	if ctx.Err() == nil {
		t.Error("closing the session did not end its calls")
	}
	if s.Context().Err() == nil {
		t.Error("the session's context outlived Close")
	}
}
