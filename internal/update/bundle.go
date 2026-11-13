package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// macOS tools (absolute paths: never looked up in $PATH).
const (
	toolHdiutil  = "/usr/bin/hdiutil"
	toolCodesign = "/usr/bin/codesign"
	toolDitto    = "/usr/bin/ditto"
	toolOpen     = "/usr/bin/open"
	toolPlutil   = "/usr/bin/plutil"
	toolXattr    = "/usr/bin/xattr"
	toolSh       = "/bin/sh"
)

// quarantineAttr is the attribute that makes Gatekeeper check a file at its first open; an
// ad-hoc signed app that carries it does not start at all.
const quarantineAttr = "com.apple.quarantine"

// macAppName is the app bundle inside the disk image (scripts/build-macos.sh).
const macAppName = "Downex.app"

// macInstaller replaces the running Downex.app with the one in the release's disk image
// (docs/M6-SPEC.md §4): it mounts the image read-only, checks that the new bundle's code
// signature is valid and has the running bundle's identifier and, for a running build
// signed with a Developer ID, its team (verify; for an ad-hoc signed running build the new
// bundle must be the release that the signed manifest names instead, verifyAdHoc), copies
// it next to the running bundle with ditto and without quarantine, swaps the two with
// renames once downexd has paused its downloads and starts a helper that relaunches the
// daemon when this one has exited.
type macInstaller struct{ sys System }

func (m *macInstaller) FileName(v Version) string { return "Downex-" + v.String() + ".dmg" }

// macTarget is the running installation.
type macTarget struct {
	bundle      string        // the running Downex.app
	sig         codeSignature // its signature
	relaunch    string        // what the relaunch helper starts: the agent app, or downexd
	relaunchApp bool          // relaunch is an app bundle (open -n -g), else an executable
}

func (m *macInstaller) Prepare(ctx context.Context) error {
	_, err := m.target(ctx)
	return err
}

// target checks the running installation: inside Downex.app, not translocated, in a
// writable folder, with a code signature (a team's or ad-hoc) that codesign can read.
func (m *macInstaller) target(ctx context.Context) (macTarget, error) {
	l, err := m.sys.Layout()
	if err != nil {
		return macTarget{}, fmt.Errorf("update: locate the installation: %w", err)
	}
	if l.Bundle == "" {
		return macTarget{}, fmt.Errorf("%w: Downex is not running from Downex.app", ErrNotUpdatable)
	}
	if l.Translocated() {
		return macTarget{}, fmt.Errorf("%w: Downex runs from a temporary copy that macOS made of it (App Translocation); "+
			"move Downex.app into Applications and start it again", ErrNotUpdatable)
	}
	parent := filepath.Dir(l.Bundle)
	probe, err := os.MkdirTemp(parent, ".downex-update-")
	if err != nil {
		return macTarget{}, fmt.Errorf("%w: cannot write to the folder that holds Downex.app: %v", ErrNotUpdatable, err)
	}
	_ = os.Remove(probe)
	sig, err := m.signature(ctx, l.Bundle)
	if err != nil {
		return macTarget{}, fmt.Errorf("%w: read the code signature of Downex.app: %v", ErrNotUpdatable, err)
	}
	t := macTarget{bundle: l.Bundle, sig: sig, relaunch: l.Daemon()}
	if fi, err := os.Stat(l.Agent); l.Agent != "" && err == nil && fi.Mode().IsRegular() {
		// .../Downex Agent.app/Contents/MacOS/downexd-agent → .../Downex Agent.app
		t.relaunch, t.relaunchApp = filepath.Dir(filepath.Dir(filepath.Dir(l.Agent))), true
	}
	return t, nil
}

// allowUnsigned reports whether DOWNEX_ALLOW_UNSIGNED_UPDATE is set to a true value (tests:
// an ad-hoc signed running build then installs an update without its version check).
func allowUnsigned(getenv func(string) string) bool {
	ok, err := strconv.ParseBool(strings.TrimSpace(getenv(AllowUnsignedEnv)))
	return err == nil && ok
}

func (m *macInstaller) Stage(ctx context.Context, dmg string, want Version) (_ Staged, err error) {
	t, err := m.target(ctx)
	if err != nil {
		return nil, err
	}
	mnt, err := os.MkdirTemp("", "downex-update-")
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	if out, err := m.sys.Run(ctx, toolHdiutil, "attach", "-nobrowse", "-readonly", "-noautoopen", "-mountpoint", mnt, dmg); err != nil {
		_ = os.Remove(mnt)
		return nil, commandError("update: mount the disk image", out, err)
	}
	defer m.detach(mnt)

	app := filepath.Join(mnt, macAppName)
	if fi, err := os.Lstat(app); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("%w: the disk image holds no %s", ErrCodeSignature, macAppName)
	}
	if err := m.verify(ctx, app, t.sig, want); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(filepath.Dir(t.bundle), ".downex-update-")
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(stage)
		}
	}()
	staged := filepath.Join(stage, macAppName)
	if out, err := m.sys.Run(ctx, toolDitto, "--noqtn", app, staged); err != nil {
		return nil, commandError("update: copy Downex.app", out, err)
	}
	m.unquarantine(ctx, staged)
	// The copy is what gets installed: check it again.
	if err := m.verify(ctx, staged, t.sig, want); err != nil {
		return nil, err
	}
	return &stagedBundle{sys: m.sys, target: t, stage: stage, staged: staged}, nil
}

// detach unmounts the disk image at mnt (by force when it is busy) and removes the
// mount point.
func (m *macInstaller) detach(mnt string) {
	ctx := context.Background()
	if out, err := m.sys.Run(ctx, toolHdiutil, "detach", mnt); err != nil {
		if out2, err2 := m.sys.Run(ctx, toolHdiutil, "detach", "-force", mnt); err2 != nil {
			m.sys.Logger.Warn("cannot unmount the update's disk image", "mountpoint", mnt,
				"err", commandError("detach", out, err), "force", commandError("detach -force", out2, err2))
			return
		}
	}
	_ = os.Remove(mnt)
}

// unquarantine removes the quarantine attribute from every file of the staged bundle: a
// quarantined ad-hoc signed app does not start, and neither would the relaunched daemon.
// The package downexd downloaded carries none (Download marks nothing) and ditto --noqtn
// copies none, so this matters only for an image that was quarantined some other way.
// xattr also fails when no file has the attribute, so a failure is only logged.
func (m *macInstaller) unquarantine(ctx context.Context, app string) {
	if out, err := m.sys.Run(ctx, toolXattr, "-dr", quarantineAttr, app); err != nil {
		m.sys.Logger.Debug("xattr removed no quarantine attribute from the update", "err", commandError("xattr -dr", out, err))
	}
}

// verify checks the code signature of the bundle app against the running one's cur: a
// valid, strict, deep signature with cur's identifier and, for a team-signed cur, cur's
// team, tested as a code requirement (so that it rests on the certificate, not on the
// displayed fields). An ad-hoc signed cur has no team to compare: verifyAdHoc then checks
// that app is the release want.
func (m *macInstaller) verify(ctx context.Context, app string, cur codeSignature, want Version) error {
	if out, err := m.sys.Run(ctx, toolCodesign, "--verify", "--deep", "--strict", app); err != nil {
		return fmt.Errorf("%w: %v", ErrCodeSignature, commandError("codesign --verify", out, err))
	}
	sig, err := m.signature(ctx, app)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCodeSignature, err)
	}
	if sig.identifier != cur.identifier {
		return fmt.Errorf("%w: the update's identifier is %q, this installation's %q", ErrCodeSignature, sig.identifier, cur.identifier)
	}
	if cur.team == "" {
		return m.verifyAdHoc(ctx, app, want)
	}
	if sig.team != cur.team {
		team := sig.team
		if team == "" {
			team = "nobody (ad-hoc)"
		}
		return fmt.Errorf("%w: the update is signed by %s, this installation by team %s", ErrCodeSignature, team, cur.team)
	}
	req := fmt.Sprintf(`=anchor apple generic and identifier "%s" and certificate leaf[subject.OU] = "%s"`, cur.identifier, cur.team)
	if out, err := m.sys.Run(ctx, toolCodesign, "--verify", "--deep", "--strict", "-R", req, app); err != nil {
		return fmt.Errorf("%w: %v", ErrCodeSignature, commandError("codesign requirement", out, err))
	}
	return nil
}

// verifyAdHoc is the rest of verify for an ad-hoc signed running build, which no signing
// team ties to its publisher. Its trust anchor is the update's manifest instead: downexd
// verified the manifest's ed25519 signature (PublicKey) and the disk image's size and
// SHA-256 from it before Stage, and verify checked the bundle's own signature and
// identifier. What is left is that the bundle is the release the manifest names: its
// CFBundleShortVersionString is want's MAJOR.MINOR.PATCH (scripts/build-macos.sh writes
// that form, bundle_version). With DOWNEX_ALLOW_UNSIGNED_UPDATE (tests) the version is not
// checked.
func (m *macInstaller) verifyAdHoc(ctx context.Context, app string, want Version) error {
	if allowUnsigned(m.sys.Getenv) {
		m.sys.Logger.Warn("installing an update without a version check: this Downex is ad-hoc signed and " + AllowUnsignedEnv + " is set")
		return nil
	}
	got, err := m.bundleVersion(ctx, app)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCodeSignature, err)
	}
	if exp := coreVersion(want); got != exp {
		return fmt.Errorf("%w: the update's Downex.app is version %s, the update description names %s", ErrCodeSignature, got, exp)
	}
	return nil
}

// coreVersion is v's MAJOR.MINOR.PATCH (a bundle's version has no pre-release).
func coreVersion(v Version) string { return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) }

// bundleVersionPattern is a CFBundleShortVersionString as the build writes it.
var bundleVersionPattern = regexp.MustCompile(`^[0-9]{1,9}\.[0-9]{1,9}\.[0-9]{1,9}$`)

// bundleVersion reads the CFBundleShortVersionString of the bundle app with plutil, which
// reads the XML and the binary form alike.
func (m *macInstaller) bundleVersion(ctx context.Context, app string) (string, error) {
	plist := filepath.Join(app, "Contents", "Info.plist")
	out, err := m.sys.Run(ctx, toolPlutil, "-extract", "CFBundleShortVersionString", "raw", "-o", "-", plist)
	if err != nil {
		return "", commandError("read the version of the update's Downex.app", out, err)
	}
	v := strings.TrimSpace(string(out))
	if !bundleVersionPattern.MatchString(v) {
		return "", fmt.Errorf("the update's Downex.app has no usable version (%q)", truncate(v, 32))
	}
	return v, nil
}

// codeSignature is what "codesign -dv" shows of a signature.
type codeSignature struct {
	identifier string
	team       string // "" for ad-hoc signatures
}

var (
	teamPattern       = regexp.MustCompile(`^[A-Z0-9]{10}$`)
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,254}$`)
)

// signature reads the code signature of path with "codesign -dv --verbose=2".
func (m *macInstaller) signature(ctx context.Context, path string) (codeSignature, error) {
	out, err := m.sys.Run(ctx, toolCodesign, "-dv", "--verbose=2", path)
	if err != nil {
		return codeSignature{}, commandError("codesign -dv", out, err)
	}
	return parseCodesign(out)
}

// parseCodesign parses the output of "codesign -dv --verbose=2": Identifier= and
// TeamIdentifier= ("not set", or a Signature=adhoc / flags "adhoc" line, for ad-hoc
// signatures). Identifiers and teams that a code requirement could not quote safely are
// refused.
func parseCodesign(out []byte) (codeSignature, error) {
	var sig codeSignature
	adhoc := false
	for line := range strings.Lines(string(out)) {
		line = strings.TrimSpace(line)
		k, v, ok := strings.Cut(line, "=")
		switch {
		case !ok:
		case k == "Identifier":
			sig.identifier = v
		case k == "TeamIdentifier":
			if v != "not set" {
				sig.team = v
			}
		case k == "Signature" && v == "adhoc":
			adhoc = true
		case k == "CodeDirectory v" && strings.Contains(v, "(adhoc"):
			adhoc = true
		}
	}
	if adhoc {
		sig.team = ""
	}
	if !identifierPattern.MatchString(sig.identifier) {
		return codeSignature{}, errors.New("codesign shows no usable identifier")
	}
	if sig.team != "" && !teamPattern.MatchString(sig.team) {
		return codeSignature{}, fmt.Errorf("codesign shows an unexpected team identifier %q", truncate(sig.team, 32))
	}
	return sig, nil
}

type stagedBundle struct {
	sys    System
	target macTarget
	stage  string // the staging folder next to the bundle
	staged string // the verified copy of the new bundle in stage
}

// Commit swaps the bundles with two renames in the bundle's folder (the old one is moved
// aside first and put back when the new one cannot take its place), removes the old one
// and starts the relaunch helper, also after a failed swap (then the old version starts
// again).
func (s *stagedBundle) Commit() error {
	bundle := s.target.bundle
	old := s.stage + ".old"
	swapErr := func() error {
		if err := os.Rename(bundle, old); err != nil {
			return fmt.Errorf("update: move the old Downex.app aside: %w", err)
		}
		if err := os.Rename(s.staged, bundle); err != nil {
			if rerr := os.Rename(old, bundle); rerr != nil {
				s.sys.Logger.Error("cannot put the old Downex.app back", "path", old, "err", rerr)
			}
			return fmt.Errorf("update: move the new Downex.app into place: %w", err)
		}
		return nil
	}()
	for _, p := range []string{old, s.stage} {
		if err := os.RemoveAll(p); err != nil {
			s.sys.Logger.Warn("cannot remove a leftover of the update", "path", p, "err", err)
		}
	}
	relaunchErr := s.relaunch()
	if swapErr != nil {
		return swapErr
	}
	return relaunchErr
}

func (s *stagedBundle) Discard() {
	if err := os.RemoveAll(s.stage); err != nil {
		s.sys.Logger.Warn("cannot remove the staged update", "path", s.stage, "err", err)
	}
}

// relaunchScript waits (at most a minute) for the daemon's process to exit, then starts
// the daemon again: the agent app through Launch Services, or downexd itself. Its
// arguments are positional ($1 pid, $2 what to start, $3 "app" or "exe"): nothing is
// interpolated into the script.
const relaunchScript = `pid=$1; target=$2; mode=$3; i=0
while kill -0 "$pid" 2>/dev/null; do
	i=$((i+1))
	[ "$i" -gt 600 ] && break
	sleep 0.1
done
if [ "$mode" = app ]; then
	exec ` + toolOpen + ` -n -g "$target" --args --background
fi
exec "$target" --background
`

// relaunch starts the relaunch helper.
func (s *stagedBundle) relaunch() error {
	mode := "exe"
	if s.target.relaunchApp {
		mode = "app"
	}
	if err := s.sys.StartDetached(toolSh, "-c", relaunchScript, "downex-relaunch", strconv.Itoa(s.sys.Pid), s.target.relaunch, mode); err != nil {
		return fmt.Errorf("update: start Downex again: %w", err)
	}
	return nil
}
