//go:build windows

package avscan

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
)

const (
	// createNoWindow keeps a console scanner from opening a console window
	// (CREATE_NO_WINDOW); downexd itself has none.
	createNoWindow = 0x08000000
	// maxCommandLine is the longest command line CreateProcess accepts, in UTF-16
	// code units including the terminating NUL.
	maxCommandLine = 32767
)

// checkCommandPlatform accepts programs only: .bat and .cmd files would be run by
// the command interpreter, which expands % and other characters of the file path.
func checkCommandPlatform(command string) error {
	switch strings.ToLower(filepath.Ext(command)) {
	case ".exe", ".com":
		return nil
	}
	return errors.New("the scanner command must be a program (.exe)")
}

// checkArgsPlatform: a Windows command line is parsed by the scanner itself; it is
// passed on as written.
func checkArgsPlatform(string) error { return nil }

// commandLine returns the scanner's command line: the quoted command, then args with
// every [File] (also one the user already put in double quotes) replaced by the
// quoted file path. Windows paths cannot contain double quotes, and a file path never
// ends with a backslash, so the quoting is exact.
func commandLine(command, args, file string) string {
	quoted := `"` + file + `"`
	args = replacePlaceholder(replaceQuotedPlaceholder(args, quoted), quoted)
	line := syscall.EscapeArg(command)
	if args = strings.TrimSpace(args); args != "" {
		line += " " + args
	}
	return line
}

// replaceQuotedPlaceholder replaces every "[File]" (with its quotes) of s by repl.
func replaceQuotedPlaceholder(s, repl string) string {
	var b strings.Builder
	quotedLen := len(Placeholder) + 2
	for {
		i := indexFold(s, `"`+Placeholder+`"`)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		b.WriteString(repl)
		s = s[i+quotedLen:]
	}
}

func setCommandLine(cmd *exec.Cmd, args, file string) error {
	line := commandLine(cmd.Path, args, file)
	if len(utf16.Encode([]rune(line))) >= maxCommandLine {
		return errors.New("the scanner command line is too long")
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: line, HideWindow: true, CreationFlags: createNoWindow}
	return nil
}
