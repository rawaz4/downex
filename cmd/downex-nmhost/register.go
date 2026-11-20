package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"downex/internal/client"
)

// Registration (docs/BROWSER-SPEC.md §1): a host manifest per browser and, on
// Windows, a registry key per browser whose default value is the manifest's path.
const (
	hostName        = "com.downex.host"
	hostDescription = "Downex: hands browser downloads to the Downex daemon"
	// backupValueName holds a key's previous default value, replaced by register and
	// restored by unregister.
	backupValueName = "DownexBackup"
	// backupSuffix marks the copy of a replaced manifest file.
	backupSuffix = ".bak"
)

// defaultFirefoxIDs are the Firefox add-on IDs allowed to connect unless --firefox-extension-id
// names others: the add-on's permanent ID on addons.mozilla.org (FIREFOX_ID in ext/wxt.config.ts).
var defaultFirefoxIDs = []string{"extension@downex.app"}

// revokedFirefoxIDs were allowed by earlier versions and are no longer: browser@downex.local,
// the ID of copies loaded by hand before the AMO listing, is a name in a domain Downex does not
// own, which any add-on can claim (BROWSER-SPEC §1). register leaves them out of the manifests it
// writes, --keep drops them from the manifests it keeps; only --firefox-extension-id brings one back.
var revokedFirefoxIDs = []string{"browser@downex.local"}

// allBrowsers are the supported browsers, in the order they are reported.
var allBrowsers = []string{"chrome", "edge", "chromium", "brave", "firefox"}

// chromeIDPattern matches Chromium extension IDs: 32 letters a-p.
var chromeIDPattern = regexp.MustCompile(`^[a-p]{32}$`)

// firefoxIDPattern matches add-on IDs: an e-mail-like name or a {GUID}.
var firefoxIDPattern = regexp.MustCompile(`^([A-Za-z0-9._+-]+@[A-Za-z0-9._-]+|\{[0-9A-Fa-f-]{36}\})$`)

// target is where one browser looks for the host.
type target struct {
	browser string
	// file is the host manifest: the file the browser reads (Linux, macOS), or the file
	// the registry value names (Windows).
	file string
	// regKey, on Windows, is the key below the registry root whose default value is file.
	regKey string
	// profile, when set, is a directory that exists when the browser was used by this
	// user; register without --browsers selects only those browsers.
	profile string
}

// regStore is the Windows registry below one root (HKCU or HKLM). Values are strings;
// name "" is a key's default value.
type regStore interface {
	root() string
	value(key, name string) (v string, ok bool, err error)
	setValue(key, name, v string) error
	deleteValue(key, name string) error
	deleteKey(key string) error
}

// installation says where the registration of each browser goes.
type installation struct {
	scope   string // "the current user" or "all users", for messages
	targets map[string]target
	reg     regStore // nil where browsers read manifests from fixed directories
	// samePath compares paths as the file system does.
	samePath func(a, b string) bool
	// protect, when set (all users on Windows), checks the manifest files (and their
	// backups) before register plans anything, and returns the step that makes their
	// directories changeable only by administrators, or nil when they already are.
	// Elsewhere the browsers' system directories belong to root already.
	protect func(files []string) (*step, error)
}

// regOptions are the parsed flags of register.
type regOptions struct {
	exe        string   // absolute path of this host
	browsers   []string // selected browsers
	chromeIDs  []string // Chromium extension IDs allowed to connect
	firefoxIDs []string // Firefox add-on IDs allowed to connect
	dryRun     bool
	// keep (--keep, the macOS window's registration at its start): a manifest of this host
	// keeps the IDs it allows and gets the missing ones of chromeIDs/firefoxIDs added; one
	// that names this executable and allows them all is left as it is, and a .bak that is
	// there already is never replaced.
	keep bool
}

// manifest is a native-messaging host manifest. Chromium browsers read
// allowed_origins, Firefox allowed_extensions.
type manifest struct {
	Name              string   `json:"name"`
	Description       string   `json:"description"`
	Path              string   `json:"path"`
	Type              string   `json:"type"`
	AllowedOrigins    []string `json:"allowed_origins,omitempty"`
	AllowedExtensions []string `json:"allowed_extensions,omitempty"`
}

func manifestFor(browser string, o regOptions) []byte {
	m := manifest{Name: hostName, Description: hostDescription, Path: o.exe, Type: "stdio"}
	if browser == "firefox" {
		m.AllowedExtensions = slices.Clone(o.firefoxIDs)
	} else {
		for _, id := range o.chromeIDs {
			m.AllowedOrigins = append(m.AllowedOrigins, "chrome-extension://"+id+"/")
		}
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return append(b, '\n')
}

// keptManifest is the manifest of browser for o.keep given the existing manifest cur, which is
// one of this host's: cur's allowed IDs (without revokedFirefoxIDs) followed by those of o that
// it lacks, and the path of o.
// same reports that cur already names o.exe (samePath) and allows every ID of o.
func keptManifest(browser string, o regOptions, cur []byte, samePath func(a, b string) bool) (data []byte, same bool) {
	var m manifest
	_ = json.Unmarshal(cur, &m) // ourManifest parsed it
	k := o
	if browser == "firefox" {
		kept := slices.DeleteFunc(slices.Clone(m.AllowedExtensions), func(id string) bool { return slices.Contains(revokedFirefoxIDs, id) })
		k.firefoxIDs = union(kept, o.firefoxIDs)
		same = slices.Equal(k.firefoxIDs, m.AllowedExtensions)
	} else {
		var ids []string
		for _, origin := range m.AllowedOrigins {
			if id, ok := strings.CutPrefix(origin, "chrome-extension://"); ok {
				ids = append(ids, strings.TrimSuffix(id, "/"))
			}
		}
		k.chromeIDs = union(ids, o.chromeIDs)
		same = len(k.chromeIDs) == len(ids) && len(ids) == len(m.AllowedOrigins)
	}
	same = same && samePath(m.Path, o.exe) && m.Type == "stdio"
	return manifestFor(browser, k), same
}

// fileExists reports that path is there (whatever it is, or when it cannot be checked:
// --keep then leaves it).
func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return !errors.Is(err, fs.ErrNotExist)
}

// union is a followed by the elements of b that a does not have, each once.
func union(a, b []string) []string {
	var out []string
	for _, s := range slices.Concat(a, b) {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// ourManifest reports a manifest of this host: its name is ours and it starts a
// downex-nmhost executable (wherever it is installed). The rule is the same on every
// OS: the path's last element after a slash or backslash, so that tests check
// Windows paths everywhere.
func ourManifest(data []byte) bool {
	var m manifest
	if json.Unmarshal(data, &m) != nil || m.Name != hostName {
		return false
	}
	base := m.Path[strings.LastIndexAny(m.Path, `/\`)+1:]
	return strings.TrimSuffix(strings.ToLower(base), ".exe") == "downex-nmhost"
}

// plan is a list of changes, printed before they are made.
type plan struct {
	steps []step
	notes []string // entries left alone, and why
}

type step struct {
	desc string
	do   func() error
}

func (p *plan) add(do func() error, format string, args ...any) {
	p.steps = append(p.steps, step{desc: fmt.Sprintf(format, args...), do: do})
}

func (p *plan) note(format string, args ...any) {
	p.notes = append(p.notes, fmt.Sprintf(format, args...))
}

// run prints the plan and, unless dryRun, carries it out, stopping at the first failure.
func (p *plan) run(w io.Writer, dryRun bool, nothing string) error {
	for _, n := range p.notes {
		fmt.Fprintf(w, "  unchanged: %s\n", n)
	}
	if len(p.steps) == 0 {
		fmt.Fprintln(w, nothing)
		return nil
	}
	fmt.Fprintln(w, "Plan:")
	for i, s := range p.steps {
		fmt.Fprintf(w, "  %d. %s\n", i+1, s.desc)
	}
	if dryRun {
		fmt.Fprintln(w, "Dry run: nothing was changed.")
		return nil
	}
	for i, s := range p.steps {
		if err := s.do(); err != nil {
			fmt.Fprintf(w, "  failed: %d. %s\n", i+1, s.desc)
			return fmt.Errorf("%s: %w", s.desc, err)
		}
		fmt.Fprintf(w, "  done: %d. %s\n", i+1, s.desc)
	}
	fmt.Fprintf(w, "Done: %d change(s).\n", len(p.steps))
	return nil
}

// planRegister plans the registration of o.exe for o.browsers in inst: manifests
// that differ are backed up (<file>.bak) and rewritten, registry values that differ
// are kept in the DownexBackup value and replaced. Identical entries are left alone.
func planRegister(inst installation, o regOptions) (*plan, error) {
	p := &plan{}
	if inst.protect != nil {
		var files []string
		for _, b := range o.browsers {
			f := inst.targets[b].file
			files = append(files, f, f+backupSuffix)
		}
		s, err := inst.protect(files)
		if err != nil {
			return nil, err
		}
		if s != nil {
			p.steps = append(p.steps, *s)
		}
	}
	for _, b := range o.browsers {
		t := inst.targets[b]
		data := manifestFor(b, o)
		cur, err := os.ReadFile(t.file)
		same := err == nil && bytes.Equal(cur, data)
		if err == nil && !same && o.keep && ourManifest(cur) {
			data, same = keptManifest(b, o, cur, inst.samePath)
		}
		switch {
		case same:
			p.note("%s manifest %s", b, t.file)
		case err == nil:
			if !o.keep || !fileExists(t.file+backupSuffix) {
				p.add(func() error { return writeFileAtomic(t.file+backupSuffix, cur) },
					"back up the existing %s manifest to %s", b, t.file+backupSuffix)
			}
			p.add(func() error { return writeFileAtomic(t.file, data) }, "replace the %s manifest %s", b, t.file)
		case errors.Is(err, fs.ErrNotExist):
			p.add(func() error { return writeFileAtomic(t.file, data) }, "write the %s manifest %s", b, t.file)
		default:
			return nil, fmt.Errorf("read %s: %w", t.file, err)
		}
		if t.regKey == "" {
			continue
		}
		key := inst.reg.root() + `\` + t.regKey
		old, ok, err := inst.reg.value(t.regKey, "")
		switch {
		case err != nil:
			return nil, fmt.Errorf("read %s: %w", key, err)
		case ok && inst.samePath(old, t.file):
			p.note("%s registry key %s", b, key)
			continue
		case ok:
			p.add(func() error { return inst.reg.setValue(t.regKey, backupValueName, old) },
				"back up the previous value of %s (%s) to its %s value", key, old, backupValueName)
		}
		p.add(func() error { return inst.reg.setValue(t.regKey, "", t.file) }, "set %s to %s", key, t.file)
	}
	return p, nil
}

// planUnregister plans the removal of this host's registration for browsers: registry
// keys are deleted only when they name this host's manifest (a backed-up previous
// value is restored instead), manifest files only when they are this host's (a .bak
// copy is restored instead).
func planUnregister(inst installation, browsers []string) (*plan, error) {
	p := &plan{}
	for _, b := range browsers {
		t := inst.targets[b]
		if t.regKey != "" {
			key := inst.reg.root() + `\` + t.regKey
			cur, ok, err := inst.reg.value(t.regKey, "")
			switch {
			case err != nil:
				return nil, fmt.Errorf("read %s: %w", key, err)
			case !ok:
			case !inst.samePath(cur, t.file):
				p.note("%s registry key %s: it names %s, not this host's manifest", b, key, cur)
			default:
				prev, hasPrev, err := inst.reg.value(t.regKey, backupValueName)
				if err != nil {
					return nil, fmt.Errorf("read %s: %w", key, err)
				}
				if hasPrev && prev != "" {
					p.add(func() error {
						if err := inst.reg.setValue(t.regKey, "", prev); err != nil {
							return err
						}
						return inst.reg.deleteValue(t.regKey, backupValueName)
					}, "restore the previous value of %s (%s)", key, prev)
				} else {
					p.add(func() error { return inst.reg.deleteKey(t.regKey) }, "delete %s", key)
				}
			}
		}
		data, err := os.ReadFile(t.file)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return nil, fmt.Errorf("read %s: %w", t.file, err)
		case !ourManifest(data):
			p.note("%s manifest %s: it is not a manifest of downex-nmhost", b, t.file)
			continue
		}
		bak := t.file + backupSuffix
		prev, err := os.ReadFile(bak)
		switch {
		case err == nil && ourManifest(prev):
			// An older registration of this host: restoring it would register it again.
			p.add(func() error { return errors.Join(os.Remove(t.file), os.Remove(bak)) },
				"remove the %s manifest %s and its backup %s (an older downex-nmhost registration)", b, t.file, bak)
		case err == nil:
			p.add(func() error {
				if err := writeFileAtomic(t.file, prev); err != nil {
					return err
				}
				return os.Remove(bak)
			}, "restore the previous %s manifest %s from %s", b, t.file, bak)
		default:
			p.add(func() error { return os.Remove(t.file) }, "remove the %s manifest %s", b, t.file)
		}
	}
	return p, nil
}

// writeFileAtomic replaces path with data (mode 0644), creating its directory.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".nmhost-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp, 0o644)
	}
	if werr == nil {
		werr = os.Rename(tmp, path)
	}
	if werr != nil {
		_ = os.Remove(tmp)
	}
	return werr
}

// ---- commands -------------------------------------------------------------------

// listFlag collects a repeatable flag whose values may also be comma-separated lists,
// the form of DefaultChromeExtensionIDs.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = appendIDs(*l, v); return nil }

// appendIDs appends the comma-separated extension IDs of s to ids. Spaces around an ID
// are dropped and an ID already there is not added again. An empty entry is kept as ""
// so that checkIDs refuses a list such as "a,,b" instead of quietly registering what is
// left of it; a blank s adds nothing.
func appendIDs(ids []string, s string) []string {
	if strings.TrimSpace(s) == "" {
		return ids
	}
	for _, id := range strings.Split(s, ",") {
		id = strings.TrimSpace(id)
		if id == "" || !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// selectBrowsers parses --browsers; without it, every browser that has a profile
// (all of them where none is detectable). Browsers without a location in inst (Brave
// for all users on macOS) are left out, and refused when named.
func selectBrowsers(arg string, inst installation, needProfile bool) ([]string, error) {
	if arg == "" {
		var out []string
		for _, b := range allBrowsers {
			t, ok := inst.targets[b]
			if ok && (!needProfile || t.profile == "" || isDir(t.profile)) {
				out = append(out, b)
			}
		}
		if len(out) == 0 {
			return nil, errors.New("no supported browser was found for this user; name them with --browsers")
		}
		return out, nil
	}
	var out []string
	for _, b := range strings.Split(arg, ",") {
		b = strings.ToLower(strings.TrimSpace(b))
		if b == "" {
			continue
		}
		if !slices.Contains(allBrowsers, b) {
			return nil, fmt.Errorf("unknown browser %q (want %s)", b, strings.Join(allBrowsers, ", "))
		}
		if _, ok := inst.targets[b]; !ok {
			return nil, fmt.Errorf("%s cannot be registered for %s on this system", b, inst.scope)
		}
		if !slices.Contains(out, b) {
			out = append(out, b)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("--browsers names no browser")
	}
	return out, nil
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// hostExecutable returns the absolute path of this executable, symbolic links resolved.
func hostExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate downex-nmhost: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Abs(exe)
}

func registerFlags(name string, stderr io.Writer) (*flag.FlagSet, *string, *bool, *bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	browsers := fs.String("browsers", "", "comma-separated `list` of "+strings.Join(allBrowsers, ",")+
		"\n(default: every browser with a profile of this user on Linux and macOS; all on Windows)")
	system := fs.Bool("system", false, "for all users (HKLM / system directories; needs administrator or root rights)")
	dryRun := fs.Bool("dry-run", false, "print the plan without changing anything")
	return fs, browsers, system, dryRun
}

func runRegister(args []string, stdout, stderr io.Writer) int {
	fs, browsersArg, system, dryRun := registerFlags("register", stderr)
	var chromeIDs listFlag
	fs.Var(&chromeIDs, "chrome-extension-id", "Chromium extension `ID` allowed to connect (repeatable or comma-separated; default: the IDs built in)")
	var firefoxIDs listFlag
	fs.Var(&firefoxIDs, "firefox-extension-id", "Firefox add-on `ID` allowed to connect (repeatable or comma-separated; default: "+
		strings.Join(defaultFirefoxIDs, ", ")+")")
	keep := fs.Bool("keep", false, "keep what the existing manifests of this host allow: add the missing extension IDs to them (dropping IDs earlier versions allowed and no longer do), "+
		"rewrite only those that are missing, name another path or lack an ID, and never replace a .bak that is there")
	allowUserWritable := fs.Bool("allow-user-writable", false, "with --system: register even when users other than "+
		"administrators (root) could replace downex-nmhost, downexd or a directory above them")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: downex-nmhost register [flags]\n\n"+
			"Register downex-nmhost (this executable, at its current path) as the native-messaging\n"+
			"host "+hostName+" of the browsers. Run it again after moving the executable.\n"+
			"With --system, every user's browser runs it: it must be installed where only\n"+
			"administrators (root) can change it, such as below Program Files or /usr/local,\n"+
			"or a Downex.app in /Applications owned by root.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if code, ok := parseFlags(fs, args, stderr); !ok {
		return code
	}
	exe, err := hostExecutable()
	if err != nil {
		fmt.Fprintf(stderr, "downex-nmhost: %v\n", err)
		return exitError
	}
	if runtime.GOOS == "darwin" && client.MacTranslocated(exe) {
		// The browsers would be given a path that disappears (docs/MACOS-SPEC.md §4).
		fmt.Fprintf(stderr, "downex-nmhost register: %s is a temporary copy that macOS made of Downex.app "+
			"(App Translocation): move Downex.app with the Finder, for example to /Applications, open it once, "+
			"then run register again from there\n", exe)
		return exitError
	}
	if *system {
		// Whoever can replace the host or downexd runs code as every user of the browsers.
		if err := checkSystemHost(exe); err != nil {
			if !*allowUserWritable {
				fmt.Fprintf(stderr, "downex-nmhost register: %v\nA registration for all users must name files that only "+
					"administrators (root) can change: install downex-nmhost and downexd there, or pass "+
					"--allow-user-writable to register this one anyway.\n", err)
				return exitError
			}
			fmt.Fprintf(stdout, "warning: %v (registered anyway: --allow-user-writable)\n", err)
		}
	}
	inst, err := defaultInstallation(*system)
	if err != nil {
		fmt.Fprintf(stderr, "downex-nmhost: %v\n", err)
		return exitError
	}
	browsers, err := selectBrowsers(*browsersArg, inst, !*system)
	if err != nil {
		fmt.Fprintf(stderr, "downex-nmhost register: %v\n", err)
		return exitUsage
	}
	o := regOptions{exe: exe, browsers: browsers, chromeIDs: chromeIDs, firefoxIDs: firefoxIDs, dryRun: *dryRun, keep: *keep}
	if len(o.firefoxIDs) == 0 {
		o.firefoxIDs = slices.Clone(defaultFirefoxIDs)
	}
	builtIn := len(o.chromeIDs) == 0
	if builtIn {
		o.chromeIDs = appendIDs(nil, DefaultChromeExtensionIDs)
	}
	if builtIn && len(o.chromeIDs) > 0 {
		// The built-in list alone (no browsers, no Firefox ID): when it is wrong, the build,
		// not the command line, is, so say where the IDs came from.
		if err := checkIDs(regOptions{chromeIDs: o.chromeIDs}); err != nil {
			fmt.Fprintf(stderr, "downex-nmhost register: the Chrome extension IDs built in: %v\n", err)
			return exitError
		}
	}
	// Everything else is the command line's (a missing ID included).
	if err := checkIDs(o); err != nil {
		fmt.Fprintf(stderr, "downex-nmhost register: %v\n", err)
		return exitUsage
	}
	if err := register(inst, o, stdout); err != nil {
		fmt.Fprintf(stderr, "downex-nmhost register: %v\n", err)
		return exitError
	}
	return exitOK
}

func runUnregister(args []string, stdout, stderr io.Writer) int {
	fs, browsersArg, system, dryRun := registerFlags("unregister", stderr)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: downex-nmhost unregister [flags]\n\n"+
			"Remove the registration of "+hostName+" from the browsers: only entries that name\n"+
			"downex-nmhost's manifests are removed, and entries that register replaced are restored.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if code, ok := parseFlags(fs, args, stderr); !ok {
		return code
	}
	inst, err := defaultInstallation(*system)
	if err != nil {
		fmt.Fprintf(stderr, "downex-nmhost: %v\n", err)
		return exitError
	}
	browsers, err := selectBrowsers(*browsersArg, inst, false)
	if err != nil {
		fmt.Fprintf(stderr, "downex-nmhost unregister: %v\n", err)
		return exitUsage
	}
	if err := unregister(inst, browsers, *dryRun, stdout); err != nil {
		fmt.Fprintf(stderr, "downex-nmhost unregister: %v\n", err)
		return exitError
	}
	return exitOK
}

// parseFlags parses args; ok is false when the command should exit with code.
func parseFlags(fs *flag.FlagSet, args []string, stderr io.Writer) (code int, ok bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK, false
		}
		return exitUsage, false
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "downex-nmhost %s: unexpected argument %q\n", fs.Name(), fs.Arg(0))
		return exitUsage, false
	}
	return 0, true
}

// errNoChromeID refuses a Chromium registration without an extension ID.
var errNoChromeID = errors.New("no Chrome extension ID: pass --chrome-extension-id <id> (the ID chrome://extensions " +
	"shows for the Downex extension; ext/EXTENSION_ID.txt for the extension of this repository), " +
	"or build downex-nmhost with -ldflags \"-X main.DefaultChromeExtensionIDs=<id>,<id>\"")

func checkIDs(o regOptions) error {
	chromium := slices.ContainsFunc(o.browsers, func(b string) bool { return b != "firefox" })
	if chromium && len(o.chromeIDs) == 0 {
		return errNoChromeID
	}
	for _, id := range o.chromeIDs {
		if !chromeIDPattern.MatchString(id) {
			return fmt.Errorf("invalid Chrome extension ID %q: want 32 letters a-p", truncate(id, 64))
		}
	}
	if slices.Contains(o.browsers, "firefox") {
		if len(o.firefoxIDs) == 0 {
			return errors.New("no Firefox add-on ID: pass --firefox-extension-id <id>")
		}
		for _, id := range o.firefoxIDs {
			if !firefoxIDPattern.MatchString(id) {
				return fmt.Errorf("invalid Firefox add-on ID %q: want name@domain or {GUID}", truncate(id, 64))
			}
		}
	}
	return nil
}

// register registers o.exe as the host of o.browsers in inst, printing the plan and
// what was done to w.
func register(inst installation, o regOptions, w io.Writer) error {
	fmt.Fprintf(w, "Registering %s for %s (%s).\nHost: %s\n", hostName, strings.Join(o.browsers, ", "), inst.scope, o.exe)
	if slices.ContainsFunc(o.browsers, func(b string) bool { return b != "firefox" }) {
		// Several IDs are normal (this repository's and the stores'): show which ones connect.
		fmt.Fprintf(w, "Chromium extension IDs: %s\n", strings.Join(o.chromeIDs, ", "))
	}
	if slices.Contains(o.browsers, "firefox") {
		fmt.Fprintf(w, "Firefox add-on IDs: %s\n", strings.Join(o.firefoxIDs, ", "))
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(o.exe), client.DaemonName())); err != nil {
		fmt.Fprintf(w, "warning: %s is not next to downex-nmhost; set %s for the browsers, or install it there\n",
			client.DaemonName(), client.EnvDaemon)
	}
	p, err := planRegister(inst, o)
	if err != nil {
		return err
	}
	return p.run(w, o.dryRun, "Nothing to change: "+hostName+" is already registered.")
}

// unregister removes this host's registration of browsers from inst.
func unregister(inst installation, browsers []string, dryRun bool, w io.Writer) error {
	fmt.Fprintf(w, "Unregistering %s from %s (%s).\n", hostName, strings.Join(browsers, ", "), inst.scope)
	p, err := planUnregister(inst, browsers)
	if err != nil {
		return err
	}
	return p.run(w, dryRun, "Nothing to change: "+hostName+" is not registered there.")
}
