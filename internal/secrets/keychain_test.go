package secrets

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The test binary doubles as a stand-in for /usr/bin/security: with
// DOWNEX_SECRETS_FAKE_SECURITY set to a directory it records its arguments and its stdin
// there (one JSON file per run) and then acts by DOWNEX_SECRETS_FAKE_MODE.
func TestMain(m *testing.M) {
	if dir := os.Getenv("DOWNEX_SECRETS_FAKE_SECURITY"); dir != "" {
		os.Exit(fakeSecurity(dir, os.Getenv("DOWNEX_SECRETS_FAKE_MODE")))
	}
	os.Exit(m.Run())
}

type securityCall struct {
	Args  []string `json:"args"`
	Stdin string   `json:"stdin"`
}

func fakeSecurity(dir, mode string) int {
	stdin, _ := io.ReadAll(os.Stdin)
	f, err := os.CreateTemp(dir, "call-*.json")
	if err != nil {
		return 90
	}
	_ = json.NewEncoder(f).Encode(securityCall{Args: os.Args[1:], Stdin: string(stdin)})
	_ = f.Close()
	switch mode {
	case "prompt": // the interactive prompt, wherever it goes
		fmt.Print("security> security> ")
		fmt.Fprint(os.Stderr, "security> security> ")
	case "fail": // interactive mode reports a failed command on stderr
		fmt.Fprint(os.Stderr, "security> security: SecKeychainItemCreateFromContent (<default>): The user name or passphrase you entered is not correct.\nsecurity> ")
	case "exit":
		fmt.Fprintln(os.Stderr, "security: unknown failure")
		return 1
	case "find":
		fmt.Println("zz-found")
	}
	return 0
}

// standInSecurity makes the test binary the security tool in mode and returns a
// function that reads the recorded calls.
func standInSecurity(t *testing.T, mode string) func() []securityCall {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	old := securityPath
	securityPath = exe
	t.Cleanup(func() { securityPath = old })
	t.Setenv("DOWNEX_SECRETS_FAKE_SECURITY", dir)
	t.Setenv("DOWNEX_SECRETS_FAKE_MODE", mode)
	return func() []securityCall {
		files, _ := filepath.Glob(filepath.Join(dir, "call-*.json"))
		var calls []securityCall
		for _, name := range files {
			b, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			var c securityCall
			if err := json.Unmarshal(b, &c); err != nil {
				t.Fatal(err)
			}
			calls = append(calls, c)
		}
		return calls
	}
}

// TestKeychainSetKeepsTheSecretOffTheCommandLine: the password never appears in the
// arguments of security (any process can read a command line); it is written as hex
// data (-X) to the stdin of an interactive security.
func TestKeychainSetKeepsTheSecretOffTheCommandLine(t *testing.T) {
	secret := []byte("zz-keychain-secret \"quoted\" \\ and ünïcode")
	calls := standInSecurity(t, "ok")
	k := &keychainKeyring{service: DefaultService}
	if err := k.Set("site_login:0123abcd", secret); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got := calls()
	if len(got) != 1 {
		t.Fatalf("security ran %d times, want 1", len(got))
	}
	c := got[0]
	hexSecret := hex.EncodeToString(secret)
	for _, a := range c.Args {
		if strings.Contains(a, "zz-keychain-secret") || strings.Contains(strings.ToLower(a), hexSecret) {
			t.Fatalf("the secret is on the command line: %q", c.Args)
		}
	}
	if !slices.Contains(c.Args, "-i") {
		t.Errorf("args = %q, want interactive mode (-i)", c.Args)
	}
	want := "add-generic-password -a site_login:0123abcd -s Downex -U -X " + hexSecret + "\n"
	if c.Stdin != want {
		t.Errorf("stdin = %q, want %q", c.Stdin, want)
	}
}

func TestKeychainSetReportsFailures(t *testing.T) {
	secret := []byte("zz-keychain-secret")
	k := &keychainKeyring{service: DefaultService}
	standInSecurity(t, "prompt")
	if err := k.Set("proxy", secret); err != nil {
		t.Errorf("Set with interactive prompts = %v, want success", err)
	}
	for _, mode := range []string{"fail", "exit"} {
		standInSecurity(t, mode)
		err := k.Set("proxy", secret)
		if err == nil {
			t.Errorf("%s: Set succeeded", mode)
			continue
		}
		if s := err.Error(); strings.Contains(s, "zz-keychain-secret") || strings.Contains(s, hex.EncodeToString(secret)) {
			t.Errorf("%s: the error repeats the secret: %v", mode, err)
		}
	}
}

func TestKeychainSetRejectsWhatSecurityCannotRead(t *testing.T) {
	k := &keychainKeyring{service: DefaultService}
	calls := standInSecurity(t, "ok")
	// The interactive line of security is limited (4096 bytes).
	if err := k.Set("proxy", []byte(strings.Repeat("x", 4000))); err == nil {
		t.Error("Set accepted a secret longer than an interactive line")
	}
	if err := (&keychainKeyring{service: "Downex Service"}).Set("proxy", []byte("x")); err == nil {
		t.Error("Set accepted a service name that would split the command")
	}
	if n := len(calls()); n != 0 {
		t.Errorf("security ran %d times", n)
	}
}

func TestKeychainGetAndDelete(t *testing.T) {
	k := &keychainKeyring{service: DefaultService}
	calls := standInSecurity(t, "find")
	got, err := k.Get("proxy")
	if err != nil || string(got) != "zz-found" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if err := k.Delete("proxy"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	var args [][]string
	for _, c := range calls() {
		args = append(args, c.Args)
	}
	slices.SortFunc(args, func(a, b []string) int { return strings.Compare(strings.Join(a, " "), strings.Join(b, " ")) })
	want := [][]string{
		{"delete-generic-password", "-a", "proxy", "-s", "Downex"},
		{"find-generic-password", "-a", "proxy", "-s", "Downex", "-w"},
	}
	if !slices.EqualFunc(args, want, slices.Equal) {
		t.Errorf("security calls = %q, want %q", args, want)
	}
}
