package update

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"downex/internal/client"
)

// starts records StartDetached calls.
type starts struct {
	mu    sync.Mutex
	calls [][]string
	err   error
}

func (s *starts) start(exe string, args ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, append([]string{exe}, args...))
	return s.err
}

func (s *starts) get() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

func noEnv(string) string { return "" }

func TestUnsupportedInstaller(t *testing.T) {
	for _, goos := range []string{"linux", "freebsd"} {
		in := NewInstaller(System{GOOS: goos})
		if err := in.Prepare(context.Background()); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: Prepare = %v, want ErrUnsupported", goos, err)
		}
		if _, err := in.Stage(context.Background(), "x", Version{Major: 1}); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: Stage = %v, want ErrUnsupported", goos, err)
		}
	}
}

func TestWindowsInstaller(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "downexd.exe")
	setup := filepath.Join(t.TempDir(), "Downex-Setup-1.0.0.exe")
	// signed: a file's Authenticode result; a file not listed is unsigned.
	type sig struct {
		signer Signer
		err    error
	}
	signed := map[string]sig{}
	var st starts
	in := NewInstaller(System{
		GOOS:          "windows",
		Layout:        func() (client.Layout, error) { return client.Layout{Exe: exe, Dir: dir}, nil },
		StartDetached: st.start,
		Getenv:        noEnv,
		Authenticode: func(p string) (Signer, error) {
			if r, ok := signed[p]; ok {
				return r.signer, r.err
			}
			return Signer{}, ErrUnsigned
		},
		Run: func(context.Context, string, ...string) ([]byte, error) {
			t.Error("the Windows installer runs no commands")
			return nil, errors.New("no")
		},
	})
	v, _ := ParseVersion("1.0.0")
	if got := in.FileName(v); got != "Downex-Setup-1.0.0.exe" {
		t.Errorf("FileName = %s", got)
	}
	ctx := context.Background()
	if err := in.Prepare(ctx); !errors.Is(err, ErrNotUpdatable) {
		t.Errorf("without uninstall.exe: %v, want ErrNotUpdatable", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "uninstall.exe"), []byte("MZ"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := in.Prepare(ctx); err != nil {
		t.Errorf("installed copy: %v", err)
	}

	// An unsigned downexd: the Setup is not checked further; Commit starts it silently.
	staged, err := in.Stage(ctx, setup, v)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.get()) != 0 {
		t.Error("Stage started something")
	}
	if err := staged.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := st.get(); len(got) != 1 || !slices.Equal(got[0], []string{setup, "/S", "/RELAUNCH"}) {
		t.Errorf("started %q, want [%s /S /RELAUNCH]", got, setup)
	}

	// A signed downexd needs a Setup validly signed by the same publisher: the same
	// certificate subject (a renewed certificate keeps it; its thumbprint changes).
	downex := Signer{Subject: "Downex", SubjectName: []byte("CN=Downex, O=Downex, C=NL")}
	renewed := downex
	other := Signer{Subject: "Downex", SubjectName: []byte("CN=Downex, O=Someone Else, C=US")}
	signed[exe] = sig{signer: downex}
	signed[setup] = sig{err: errors.New("TRUST_E_BAD_DIGEST")}
	if _, err := in.Stage(ctx, setup, v); !errors.Is(err, ErrCodeSignature) {
		t.Errorf("signed downexd, badly signed Setup: %v, want ErrCodeSignature", err)
	}
	delete(signed, setup) // unsigned
	if _, err := in.Stage(ctx, setup, v); !errors.Is(err, ErrCodeSignature) {
		t.Errorf("signed downexd, unsigned Setup: %v, want ErrCodeSignature", err)
	}
	signed[setup] = sig{signer: other}
	if _, err := in.Stage(ctx, setup, v); !errors.Is(err, ErrCodeSignature) {
		t.Errorf("Setup validly signed by another publisher: %v, want ErrCodeSignature", err)
	}
	signed[setup] = sig{signer: Signer{Subject: "Downex"}}
	if _, err := in.Stage(ctx, setup, v); !errors.Is(err, ErrCodeSignature) {
		t.Errorf("Setup signer subject unreadable: %v, want ErrCodeSignature", err)
	}
	signed[exe] = sig{signer: Signer{}}
	signed[setup] = sig{signer: Signer{}}
	if _, err := in.Stage(ctx, setup, v); !errors.Is(err, ErrCodeSignature) {
		t.Errorf("neither signer subject readable: %v, want ErrCodeSignature", err)
	}
	signed[exe] = sig{signer: downex}
	signed[setup] = sig{signer: renewed}
	if _, err := in.Stage(ctx, setup, v); err != nil {
		t.Errorf("both signed by the same publisher: %v", err)
	}

	st.err = errors.New("access denied")
	staged, _ = in.Stage(ctx, setup, v)
	if err := staged.Commit(); err == nil {
		t.Error("a failed start was not reported")
	}
}

// macFixture is a stand-in installation: <root>/Applications/Downex.app with a MARK file
// ("old"; the disk image's bundle has "new"), the agent and Helpers/downexd.
type macFixture struct {
	t        *testing.T
	root     string
	apps     string
	bundle   string
	agentExe string
	dmg      string
	want     Version // the manifest's version (1.0.0)
	env      map[string]string
	st       starts

	mu         sync.Mutex
	commands   [][]string
	sigs       map[string]string // MARK → codesign -dv output
	verifyFail map[string]bool   // MARK → codesign --verify fails
	reqFail    bool              // the -R requirement fails
	versions   map[string]string // MARK → the CFBundleShortVersionString plutil prints
	plutilFail bool              // plutil fails
	mounted    []string
	detached   []string
}

const (
	devIDOld = "Executable=/x\nIdentifier=com.downex.app\nFormat=app bundle\nCodeDirectory v=20500 size=1 flags=0x10000(runtime) hashes=1\n" +
		"Authority=Developer ID Application: Example (ABCDE12345)\nTeamIdentifier=ABCDE12345\n"
	adhocSig = "Executable=/x\nIdentifier=com.downex.app\nCodeDirectory v=20400 size=1 flags=0x2(adhoc) hashes=1\nSignature=adhoc\nTeamIdentifier=not set\n"
	otherSig = "Identifier=com.downex.app\nAuthority=Developer ID Application: Someone (ZZZZZ99999)\nTeamIdentifier=ZZZZZ99999\n"
)

func newMacFixture(t *testing.T) *macFixture {
	t.Helper()
	f := &macFixture{t: t, root: t.TempDir(), env: map[string]string{},
		sigs: map[string]string{"old": devIDOld, "new": devIDOld}, verifyFail: map[string]bool{},
		versions: map[string]string{"old": "0.9.0", "new": "1.0.0"}}
	f.want, _ = ParseVersion("1.0.0")
	f.apps = filepath.Join(f.root, "Applications")
	f.bundle = filepath.Join(f.apps, "Downex.app")
	f.agentExe = writeBundle(t, f.bundle, "old")
	f.dmg = filepath.Join(f.root, "Downex-1.0.0.dmg")
	if err := os.WriteFile(f.dmg, []byte("dmg"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// writeBundle creates a stand-in Downex.app at dir and returns its agent executable.
func writeBundle(t *testing.T, dir, mark string) string {
	t.Helper()
	agent := filepath.Join(dir, "Contents", "Library", "LoginItems", client.MacAgentApp, "Contents", "MacOS", client.MacAgentName)
	for p, data := range map[string]string{
		filepath.Join(dir, "MARK"):                           mark,
		filepath.Join(dir, "Contents", "Helpers", "downexd"): "daemon",
		agent: "agent",
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return agent
}

func (f *macFixture) layout() (client.Layout, error) {
	return client.Layout{
		Exe: f.agentExe, Bundle: f.bundle, Dir: filepath.Join(f.bundle, "Contents", "Helpers"),
		Window: filepath.Join(f.bundle, "Contents", "MacOS", "Downex"), Agent: f.agentExe,
	}, nil
}

func mark(app string) string {
	b, _ := os.ReadFile(filepath.Join(app, "MARK"))
	return string(b)
}

func (f *macFixture) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, append([]string{name}, args...))
	last := args[len(args)-1]
	switch {
	case name == toolHdiutil && args[0] == "attach":
		i := slices.Index(args, "-mountpoint")
		if i < 0 || last != f.dmg || !slices.Contains(args, "-readonly") || !slices.Contains(args, "-nobrowse") {
			return []byte("bad attach"), errors.New("exit status 1")
		}
		writeBundle(f.t, filepath.Join(args[i+1], macAppName), "new")
		f.mounted = append(f.mounted, args[i+1])
		return nil, nil
	case name == toolHdiutil && args[0] == "detach":
		if err := os.RemoveAll(filepath.Join(last, macAppName)); err != nil {
			return nil, err
		}
		f.detached = append(f.detached, last)
		return nil, nil
	case name == toolDitto:
		if len(args) != 3 || args[0] != "--noqtn" {
			return []byte("bad ditto"), errors.New("exit status 1")
		}
		return nil, copyTree(args[1], args[2])
	case name == toolXattr:
		if !slices.Equal(args[:2], []string{"-dr", quarantineAttr}) {
			return []byte("bad xattr"), errors.New("exit status 2")
		}
		// No file has the attribute: xattr says so and fails, which Stage ignores.
		return []byte("No such xattr: " + quarantineAttr), errors.New("exit status 1")
	case name == toolPlutil:
		plist := filepath.Join("Contents", "Info.plist")
		if !slices.Equal(args[:len(args)-1], []string{"-extract", "CFBundleShortVersionString", "raw", "-o", "-"}) ||
			!strings.HasSuffix(last, plist) {
			return []byte("bad plutil"), errors.New("exit status 1")
		}
		if f.plutilFail {
			return []byte("Could not extract value, error: No value at that key path"), errors.New("exit status 1")
		}
		return []byte(f.versions[mark(strings.TrimSuffix(last, string(filepath.Separator)+plist))] + "\n"), nil
	case name == toolCodesign && args[0] == "-dv":
		return []byte(f.sigs[mark(last)]), nil
	case name == toolCodesign && args[0] == "--verify":
		if f.verifyFail[mark(last)] {
			return []byte(last + ": invalid signature (code or signature have been modified)"), errors.New("exit status 1")
		}
		if slices.Contains(args, "-R") && f.reqFail {
			return []byte("test-requirement: code failed to satisfy specified code requirement(s)"), errors.New("exit status 3")
		}
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected command %s %q", name, args)
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o755)
	})
}

func (f *macFixture) installer() Installer {
	return NewInstaller(System{
		GOOS: "darwin", Layout: f.layout, Run: f.run, StartDetached: f.st.start,
		Getenv: func(k string) string { return f.env[k] }, Pid: 4242,
	})
}

// leftovers lists the update's temporary folders next to the bundle.
func (f *macFixture) leftovers() []string {
	entries, _ := os.ReadDir(f.apps)
	var out []string
	for _, e := range entries {
		if e.Name() != "Downex.app" {
			out = append(out, e.Name())
		}
	}
	return out
}

func (f *macFixture) ran(name string, args ...string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.commands {
		if c[0] == name && len(c) > len(args) && slices.Equal(c[1:1+len(args)], args) {
			return true
		}
	}
	return false
}

func TestMacInstallerSameTeam(t *testing.T) {
	f := newMacFixture(t)
	in := f.installer()
	ctx := context.Background()
	v, _ := ParseVersion("1.0.0")
	if got := in.FileName(v); got != "Downex-1.0.0.dmg" {
		t.Errorf("FileName = %s", got)
	}
	if err := in.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	staged, err := in.Stage(ctx, f.dmg, f.want)
	if err != nil {
		t.Fatal(err)
	}
	if mark(f.bundle) != "old" {
		t.Error("Stage changed the installation")
	}
	if len(f.mounted) != 1 || !slices.Equal(f.detached, f.mounted) {
		t.Errorf("mounted %v, detached %v", f.mounted, f.detached)
	}
	if _, err := os.Stat(f.mounted[0]); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("mount point left behind: %v", err)
	}
	// The team is tested as a code requirement on the image's bundle and on the copy.
	f.mu.Lock()
	var reqs []string
	for _, c := range f.commands {
		if i := slices.Index(c, "-R"); i >= 0 {
			reqs = append(reqs, c[i+1])
		}
	}
	f.mu.Unlock()
	want := `=anchor apple generic and identifier "com.downex.app" and certificate leaf[subject.OU] = "ABCDE12345"`
	if len(reqs) != 2 || reqs[0] != want || reqs[1] != want {
		t.Errorf("requirements %q, want twice %q", reqs, want)
	}
	if len(f.st.get()) != 0 {
		t.Error("Stage started something")
	}

	if err := staged.Commit(); err != nil {
		t.Fatal(err)
	}
	if mark(f.bundle) != "new" {
		t.Errorf("after Commit the bundle is %q, want new", mark(f.bundle))
	}
	if left := f.leftovers(); len(left) != 0 {
		t.Errorf("left behind next to Downex.app: %v", left)
	}
	calls := f.st.get()
	agentApp := filepath.Join(f.bundle, "Contents", "Library", "LoginItems", client.MacAgentApp)
	if len(calls) != 1 || calls[0][0] != toolSh || calls[0][1] != "-c" || calls[0][2] != relaunchScript ||
		!slices.Equal(calls[0][3:], []string{"downex-relaunch", "4242", agentApp, "app"}) {
		t.Errorf("relaunch %q", calls)
	}
}

func TestMacInstallerRefusals(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		setup func(f *macFixture)
		want  error
	}{
		{"other team", func(f *macFixture) { f.sigs["new"] = otherSig }, ErrCodeSignature},
		{"ad-hoc update", func(f *macFixture) { f.sigs["new"] = adhocSig }, ErrCodeSignature},
		{"other identifier", func(f *macFixture) { f.sigs["new"] = strings.Replace(devIDOld, "com.downex.app", "com.example.app", 1) }, ErrCodeSignature},
		{"invalid signature", func(f *macFixture) { f.verifyFail["new"] = true }, ErrCodeSignature},
		{"requirement fails", func(f *macFixture) { f.reqFail = true }, ErrCodeSignature},
		// The Developer ID path checks no version: the team's signature ties it to its publisher.
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newMacFixture(t)
			c.setup(f)
			_, err := f.installer().Stage(ctx, f.dmg, f.want)
			if !errors.Is(err, c.want) {
				t.Fatalf("Stage = %v, want %v", err, c.want)
			}
			if mark(f.bundle) != "old" {
				t.Error("the installation was changed")
			}
			if left := f.leftovers(); len(left) != 0 {
				t.Errorf("left behind: %v", left)
			}
			if !slices.Equal(f.detached, f.mounted) {
				t.Errorf("mounted %v, detached %v", f.mounted, f.detached)
			}
			if len(f.st.get()) != 0 {
				t.Error("something was started")
			}
		})
	}
}

// TestMacInstallerAdHoc: an ad-hoc signed installation takes an update whose bundle is
// validly signed (ad-hoc or by a team), has its identifier and is the manifest's version;
// the manifest's signature and the image's SHA-256 were checked before Stage.
func TestMacInstallerAdHoc(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		setup func(f *macFixture)
		want  error // nil: installed
	}{
		{"ad-hoc update of the manifest's version", func(f *macFixture) {}, nil},
		{"a pre-release's bundle has its MAJOR.MINOR.PATCH", func(f *macFixture) { f.want, _ = ParseVersion("1.0.0-rc.1") }, nil},
		{"Developer ID signed update", func(f *macFixture) { f.sigs["new"] = devIDOld }, nil},
		{"another version than the manifest's", func(f *macFixture) { f.versions["new"] = "1.0.1" }, ErrCodeSignature},
		{"an older version", func(f *macFixture) { f.versions["new"] = "0.9.0" }, ErrCodeSignature},
		{"no usable version", func(f *macFixture) { f.versions["new"] = "dev" }, ErrCodeSignature},
		{"version not readable", func(f *macFixture) { f.plutilFail = true }, ErrCodeSignature},
		{"other identifier", func(f *macFixture) { f.sigs["new"] = strings.Replace(adhocSig, "com.downex.app", "com.example.app", 1) }, ErrCodeSignature},
		{"invalid signature", func(f *macFixture) { f.verifyFail["new"] = true }, ErrCodeSignature},
		{"env off: the version is checked", func(f *macFixture) { f.env[AllowUnsignedEnv] = "0"; f.versions["new"] = "2.0.0" }, ErrCodeSignature},
		{"env on (tests): no version check", func(f *macFixture) { f.env[AllowUnsignedEnv] = "1"; f.versions["new"] = "2.0.0" }, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newMacFixture(t)
			f.sigs["old"], f.sigs["new"] = adhocSig, adhocSig
			c.setup(f)
			in := f.installer()
			if err := in.Prepare(ctx); err != nil {
				t.Fatalf("Prepare = %v", err)
			}
			staged, err := in.Stage(ctx, f.dmg, f.want)
			if c.want != nil {
				if !errors.Is(err, c.want) {
					t.Fatalf("Stage = %v, want %v", err, c.want)
				}
				if mark(f.bundle) != "old" || len(f.leftovers()) != 0 || len(f.st.get()) != 0 {
					t.Errorf("a refused update changed something: bundle %q, left %v, started %q", mark(f.bundle), f.leftovers(), f.st.get())
				}
				return
			}
			if err != nil {
				t.Fatalf("Stage = %v", err)
			}
			if f.ran(toolCodesign, "--verify", "--deep", "--strict", "-R") {
				t.Error("a team requirement was tested for an ad-hoc installation")
			}
			// The copy loses any quarantine attribute (else the ad-hoc app would not start).
			stagedApp := staged.(*stagedBundle).staged
			if !f.ran(toolXattr, "-dr", quarantineAttr, stagedApp) {
				t.Error("the staged copy was not freed of the quarantine attribute")
			}
			if err := staged.Commit(); err != nil {
				t.Fatal(err)
			}
			if mark(f.bundle) != "new" {
				t.Errorf("after Commit the bundle is %q, want new", mark(f.bundle))
			}
			if left := f.leftovers(); len(left) != 0 {
				t.Errorf("left behind next to Downex.app: %v", left)
			}
			if calls := f.st.get(); len(calls) != 1 || calls[0][0] != toolSh {
				t.Errorf("relaunch %q", calls)
			}
		})
	}
}

func TestMacInstallerAdHocAllowed(t *testing.T) {
	f := newMacFixture(t)
	f.sigs["old"], f.sigs["new"] = adhocSig, adhocSig
	f.env[AllowUnsignedEnv] = "1"
	// Without the agent the daemon in Helpers is started again.
	if err := os.Remove(f.agentExe); err != nil {
		t.Fatal(err)
	}
	staged, err := f.installer().Stage(context.Background(), f.dmg, f.want)
	if err != nil {
		t.Fatal(err)
	}
	if f.ran(toolCodesign, "--verify", "--deep", "--strict", "-R") {
		t.Error("a team requirement was tested for an ad-hoc installation")
	}
	if err := staged.Commit(); err != nil {
		t.Fatal(err)
	}
	calls := f.st.get()
	daemon := filepath.Join(f.bundle, "Contents", "Helpers", "downexd")
	if len(calls) != 1 || !slices.Equal(calls[0][3:], []string{"downex-relaunch", "4242", daemon, "exe"}) {
		t.Errorf("relaunch %q", calls)
	}
}

func TestMacInstallerNotABundle(t *testing.T) {
	f := newMacFixture(t)
	for name, l := range map[string]client.Layout{
		"no bundle":    {Exe: "/usr/local/bin/downexd", Dir: "/usr/local/bin"},
		"translocated": {Exe: "/x", Bundle: "/private/var/folders/ab/T/AppTranslocation/1234/d/Downex.app"},
		"read-only":    {Exe: "/x", Bundle: filepath.Join(f.root, "missing", "Downex.app")},
	} {
		in := NewInstaller(System{GOOS: "darwin", Layout: func() (client.Layout, error) { return l, nil }, Run: f.run,
			StartDetached: f.st.start, Getenv: noEnv})
		if err := in.Prepare(context.Background()); !errors.Is(err, ErrNotUpdatable) {
			t.Errorf("%s: Prepare = %v, want ErrNotUpdatable", name, err)
		}
	}
}

func TestMacInstallerCommitFailure(t *testing.T) {
	f := newMacFixture(t)
	staged, err := f.installer().Stage(context.Background(), f.dmg, f.want)
	if err != nil {
		t.Fatal(err)
	}
	// The staged copy vanished: the old bundle stays (moved back) and is started again.
	sb := staged.(*stagedBundle)
	if err := os.RemoveAll(sb.staged); err != nil {
		t.Fatal(err)
	}
	if err := staged.Commit(); err == nil {
		t.Fatal("Commit succeeded without a staged bundle")
	}
	if mark(f.bundle) != "old" {
		t.Errorf("bundle %q after a failed swap, want old", mark(f.bundle))
	}
	if left := f.leftovers(); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
	if len(f.st.get()) != 1 {
		t.Error("the old version was not started again")
	}

	f2 := newMacFixture(t)
	staged, err = f2.installer().Stage(context.Background(), f2.dmg, f2.want)
	if err != nil {
		t.Fatal(err)
	}
	staged.Discard()
	if left := f2.leftovers(); len(left) != 0 || mark(f2.bundle) != "old" {
		t.Errorf("after Discard: %v, bundle %q", left, mark(f2.bundle))
	}
}

func TestParseCodesign(t *testing.T) {
	cases := []struct {
		out      string
		id, team string
		ok       bool
	}{
		{devIDOld, "com.downex.app", "ABCDE12345", true},
		{adhocSig, "com.downex.app", "", true},
		// An ad-hoc signature never counts as a team's, whatever else is shown.
		{"Identifier=com.downex.app\nSignature=adhoc\nTeamIdentifier=ABCDE12345\n", "com.downex.app", "", true},
		{"Identifier=com.downex.app\nTeamIdentifier=abc\" or true\n", "", "", false},
		{"TeamIdentifier=ABCDE12345\n", "", "", false},
		{"Identifier=com.downex.app\" or anchor\nTeamIdentifier=ABCDE12345\n", "", "", false},
	}
	for _, c := range cases {
		sig, err := parseCodesign([]byte(c.out))
		if (err == nil) != c.ok || (c.ok && (sig.identifier != c.id || sig.team != c.team)) {
			t.Errorf("parseCodesign(%q) = %+v, %v", c.out, sig, err)
		}
	}
}

// TestRelaunchScript runs the relaunch helper's script with a stand-in daemon.
func TestRelaunchScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs /bin/sh")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "args")
	target := filepath.Join(dir, "downexd")
	if err := os.WriteFile(target, []byte("#!/bin/sh\necho \"$@\" > '"+out+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A process that has exited: the script starts the target at once.
	done := exec.Command("/bin/sh", "-c", "exit 0")
	if err := done.Run(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(toolSh, "-c", relaunchScript, "downex-relaunch", fmt.Sprint(done.Process.Pid), target, "exe")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, err := os.ReadFile(out)
		if err == nil && strings.TrimSpace(string(b)) == "--background" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("target args %q, %v", b, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
