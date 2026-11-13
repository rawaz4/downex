package avscan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as the scanner: with DOWNEX_AVSCAN_HELPER set it records its
// arguments in DOWNEX_AVSCAN_ARGS_OUT and then, by the mode, exits with a code
// ("exit:N") or hangs ("sleep").
func TestMain(m *testing.M) {
	if mode := os.Getenv("DOWNEX_AVSCAN_HELPER"); mode != "" {
		os.Exit(helper(mode))
	}
	os.Exit(m.Run())
}

func helper(mode string) int {
	if out := os.Getenv("DOWNEX_AVSCAN_ARGS_OUT"); out != "" {
		data, _ := json.Marshal(os.Args[1:])
		if err := os.WriteFile(out, data, 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 99
		}
	}
	switch {
	case mode == "sleep":
		time.Sleep(time.Minute)
		return 0
	case strings.HasPrefix(mode, "exit:"):
		n, err := strconv.Atoi(mode[len("exit:"):])
		if err != nil {
			return 98
		}
		fmt.Println("scanned")
		fmt.Fprintln(os.Stderr, "\x1b[31mwarning\x1b[0m")
		return n
	}
	return 97
}

// scanner returns the test binary as the scanner, in the given mode, and the file its
// arguments are recorded in.
func scanner(t *testing.T, mode string) (command, argsFile string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	argsFile = filepath.Join(t.TempDir(), "args.json")
	t.Setenv("DOWNEX_AVSCAN_HELPER", mode)
	t.Setenv("DOWNEX_AVSCAN_ARGS_OUT", argsFile)
	return exe, argsFile
}

func recordedArgs(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	if err := json.Unmarshal(data, &args); err != nil {
		t.Fatal(err)
	}
	return args
}

// downloaded creates a file whose path needs quoting everywhere.
func downloaded(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "My Downloads & more")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "100% sure (1) it's 'fine'.zip")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunSubstitutesFile(t *testing.T) {
	file := downloaded(t)
	for _, tc := range []struct {
		name string
		args string
		want []string
	}{
		{"default", DefaultArgs, []string{file}},
		{"options around", "-Scan -ScanType 3 -File [File] -DisableRemediation", []string{"-Scan", "-ScanType", "3", "-File", file, "-DisableRemediation"}},
		{"already quoted", `--file "[File]" --quiet`, []string{"--file", file, "--quiet"}},
		{"inside a word", "--target=[File]", []string{"--target=" + file}},
		{"case-insensitive, twice", "[file] [FILE]", []string{file, file}},
		{"quoted other argument", `--log "a b.txt" [File]`, []string{"--log", "a b.txt", file}},
		{"no placeholder", "--all", []string{"--all"}},
		{"empty", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command, argsFile := scanner(t, "exit:0")
			res, err := Run(context.Background(), command, tc.args, file, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if res.ExitCode != 0 {
				t.Fatalf("exit code %d, want 0 (output %q)", res.ExitCode, res.Output)
			}
			if got := recordedArgs(t, argsFile); !slices.Equal(got, tc.want) && !(len(got) == 0 && len(tc.want) == 0) {
				t.Fatalf("scanner arguments = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRunExitCodes(t *testing.T) {
	file := downloaded(t)
	for _, code := range []int{0, 1, 2, 50} {
		command, _ := scanner(t, "exit:"+strconv.Itoa(code))
		res, err := Run(context.Background(), command, DefaultArgs, file, Options{})
		if err != nil {
			t.Fatalf("exit %d: %v", code, err)
		}
		if res.ExitCode != code {
			t.Fatalf("exit code %d, want %d", res.ExitCode, code)
		}
		if !strings.Contains(res.Output, "scanned") || strings.ContainsRune(res.Output, 0x1b) {
			t.Fatalf("output %q: want the scanner's text without control characters", res.Output)
		}
	}
}

func TestRunTimeout(t *testing.T) {
	file := downloaded(t)
	command, _ := scanner(t, "sleep")
	start := time.Now()
	_, err := Run(context.Background(), command, DefaultArgs, file, Options{Timeout: 300 * time.Millisecond})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("Run = %v, want ErrTimeout", err)
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Fatalf("Run took %v after the timeout", d)
	}
}

func TestRunCanceled(t *testing.T) {
	file := downloaded(t)
	command, _ := scanner(t, "sleep")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	_, err := Run(ctx, command, DefaultArgs, file, Options{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
}

func TestRunRefuses(t *testing.T) {
	file := downloaded(t)
	command, _ := scanner(t, "exit:0")
	for _, tc := range []struct {
		name, command, args, file string
	}{
		{"relative command", "scanner", DefaultArgs, file},
		{"missing command", filepath.Join(t.TempDir(), "missing"), DefaultArgs, file},
		{"directory command", t.TempDir(), DefaultArgs, file},
		{"relative file", command, DefaultArgs, "a.zip"},
		{"NUL in args", command, "a\x00b", file},
	} {
		if _, err := Run(context.Background(), tc.command, tc.args, tc.file, Options{}); err == nil {
			t.Errorf("%s: Run succeeded", tc.name)
		}
	}
}

func TestCheckCommand(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckCommand(exe); err != nil {
		t.Fatalf("CheckCommand(test binary) = %v", err)
	}
	dir := t.TempDir()
	for _, bad := range []string{"", "scan.exe", filepath.Join(dir, "missing.exe"), dir, exe + "\x00", strings.Repeat("a", MaxCommandBytes+1)} {
		if err := CheckCommand(bad); err == nil {
			t.Errorf("CheckCommand(%q) accepted", bad)
		}
	}
	script := filepath.Join(dir, "scan.cmd")
	if err := os.WriteFile(script, []byte("@echo off"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := CheckCommand(script); (err == nil) == (runtime.GOOS == "windows") {
		t.Errorf("CheckCommand(%s) = %v on %s", script, err, runtime.GOOS)
	}
}

func TestCheckArgs(t *testing.T) {
	for _, ok := range []string{"", DefaultArgs, `-a "x y" [File]`} {
		if err := CheckArgs(ok); err != nil {
			t.Errorf("CheckArgs(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"a\x00", strings.Repeat("a", MaxArgsBytes+1), "\xff"} {
		if err := CheckArgs(bad); err == nil {
			t.Errorf("CheckArgs(%q) accepted", bad)
		}
	}
	// Unbalanced quotes matter only where the arguments are split into words.
	if err := CheckArgs(`--name "x`); (err == nil) != (runtime.GOOS == "windows") {
		t.Errorf("CheckArgs(unterminated quote) = %v on %s", err, runtime.GOOS)
	}
}

func TestSplitArgs(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
		err  bool
	}{
		{"", nil, false},
		{"  a  b\tc\n", []string{"a", "b", "c"}, false},
		{`'a b' "c d"`, []string{"a b", "c d"}, false},
		{`x'y z'w`, []string{"xy zw"}, false},
		{`"a \"q\" \\ \$ \n"`, []string{`a "q" \ $ \n`}, false},
		{`a\ b \'c`, []string{"a b", "'c"}, false},
		{`'$HOME' $HOME ~ *.zip`, []string{"$HOME", "$HOME", "~", "*.zip"}, false},
		{`''`, []string{""}, false},
		{`'open`, nil, true},
		{`"open`, nil, true},
		{`trailing\`, nil, true},
	} {
		got, err := splitArgs(tc.in)
		if (err != nil) != tc.err || !slices.Equal(got, tc.want) {
			t.Errorf("splitArgs(%q) = %q, %v; want %q (error %v)", tc.in, got, err, tc.want, tc.err)
		}
	}
}

func TestReplacePlaceholder(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"[File]", "P"},
		{"a[file]b[FILE]c", "aPbPc"},
		{"[Fil]", "[Fil]"},
		{"[[File]]", "[P]"},
	} {
		if got := replacePlaceholder(tc.in, "P"); got != tc.want {
			t.Errorf("replacePlaceholder(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLimitedBuffer(t *testing.T) {
	b := &limitedBuffer{limit: 8}
	n, err := b.Write([]byte("0123456789"))
	if n != 10 || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	_, _ = b.Write([]byte("more"))
	if got := b.String(); got != "01234567" {
		t.Fatalf("kept %q", got)
	}
}
