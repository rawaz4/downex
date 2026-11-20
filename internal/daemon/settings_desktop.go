package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"downex/internal/avscan"
	downexv1 "downex/internal/rpc/downex/v1"
)

// DesktopSettings holds the M6 desktop settings (docs/M6-SPEC.md §3, Settings fields
// 17–21): the clipboard monitor, marking downloaded files with their origin and the
// antivirus scan of completed downloads. A settings file without them (or without some
// of them) gets DefaultDesktopSettings' values for what is missing.
type DesktopSettings struct {
	ClipboardMonitor    bool   `json:"clipboard_monitor"`     // offer copied download links (default off)
	MarkDownloadedFiles bool   `json:"mark_downloaded_files"` // Mark-of-the-Web / quarantine / origin xattrs (default on)
	AVScanEnabled       bool   `json:"av_scan_enabled"`       // scan completed downloads (default off)
	AVScanCommand       string `json:"av_scan_command"`       // absolute path of the scanner program
	AVScanArgs          string `json:"av_scan_args"`          // [File] = the downloaded file (default "[File]")
	// CheckFiles is the file check (docs/DESKTOP-SPEC.md §9.9, Settings field 27):
	// warn when a file is not what its name says (default on).
	CheckFiles bool `json:"check_files"`
	// RenewLinks is what a new link that serves the same file as a download whose link
	// expired does (docs/DESKTOP-SPEC.md §10, Settings field 28): "" (auto, the default;
	// a file may say "auto" too, which reads as ""), RenewLinksAsk or RenewLinksOff. Read
	// it with RenewMode.
	RenewLinks string `json:"renew_links,omitempty"`
}

// The values of DesktopSettings.RenewLinks (docs/DAEMON-SPEC.md §3 "Automatic matching").
const (
	RenewLinksAuto = "auto" // a proven new link continues the download
	RenewLinksAsk  = "ask"  // a proven new link waits for confirmation with an offer
	RenewLinksOff  = "off"  // new links are never matched
)

// DefaultDesktopSettings returns the desktop settings of a new installation.
func DefaultDesktopSettings() DesktopSettings {
	return DesktopSettings{MarkDownloadedFiles: true, AVScanArgs: avscan.DefaultArgs, CheckFiles: true}
}

// RenewMode is renew_links in effect: RenewLinksAuto, RenewLinksAsk or RenewLinksOff.
func (d DesktopSettings) RenewMode() string {
	if d.RenewLinks == "" {
		return RenewLinksAuto
	}
	return d.RenewLinks
}

// canonicalRenewLinks returns the stored form of a renew_links value ("" for auto) and
// whether it is known.
func canonicalRenewLinks(v string) (string, bool) {
	switch v {
	case "", RenewLinksAuto:
		return "", true
	case RenewLinksAsk, RenewLinksOff:
		return v, true
	}
	return "", false
}

// UnmarshalJSON decodes the settings file's "desktop" object: fields it lacks keep
// their DefaultDesktopSettings values (a file of an older daemon has none of them).
func (d *DesktopSettings) UnmarshalJSON(data []byte) error {
	type plain DesktopSettings // without this method
	v := plain(DefaultDesktopSettings())
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*d = DesktopSettings(v)
	return nil
}

// scanConfigured reports whether completed downloads are scanned.
func (d DesktopSettings) scanConfigured() bool { return d.AVScanEnabled && d.AVScanCommand != "" }

// normalize validates desktop settings from a client: empty arguments select the
// default ("[File]"); the arguments must be usable (avscan.CheckArgs). While scanning
// is enabled the command is required and must be an absolute path of an existing
// regular file (a program on Windows); while it is off a command is only checked to be
// an absolute path, so that a scanner that went away does not block other changes.
func (d DesktopSettings) normalize() (DesktopSettings, error) {
	v, ok := canonicalRenewLinks(d.RenewLinks)
	if !ok {
		return d, errors.New("renew_links must be auto, ask or off")
	}
	d.RenewLinks = v
	d.AVScanCommand = strings.TrimSpace(d.AVScanCommand)
	if strings.TrimSpace(d.AVScanArgs) == "" {
		d.AVScanArgs = avscan.DefaultArgs
	}
	if err := avscan.CheckArgs(d.AVScanArgs); err != nil {
		return d, fmt.Errorf("av_scan_args: %w", err)
	}
	switch {
	case d.AVScanEnabled && d.AVScanCommand == "":
		return d, errors.New("av_scan_command is required when av_scan_enabled is set")
	case d.AVScanEnabled:
		if err := avscan.CheckCommand(d.AVScanCommand); err != nil {
			return d, fmt.Errorf("av_scan_command: %w", err)
		}
	case d.AVScanCommand != "":
		if err := checkCommandSyntax(d.AVScanCommand); err != nil {
			return d, fmt.Errorf("av_scan_command: %w", err)
		}
	}
	if d.AVScanCommand != "" {
		d.AVScanCommand = filepath.Clean(d.AVScanCommand)
	}
	return d, nil
}

// checkCommandSyntax checks a scanner path without looking at the file system.
func checkCommandSyntax(command string) error {
	switch {
	case len(command) > avscan.MaxCommandBytes:
		return fmt.Errorf("the scanner command is longer than %d bytes", avscan.MaxCommandBytes)
	case strings.ContainsRune(command, 0) || !utf8.ValidString(command):
		return errors.New("the scanner command is not a valid path")
	case !filepath.IsAbs(command):
		return errors.New("the scanner command must be an absolute path")
	}
	return nil
}

// repair replaces invalid desktop settings read from disk and describes each
// replacement: unusable arguments become the default, an invalid command path is
// dropped (scanning is then off). Whether the scanner exists is not checked here (its
// drive may not be mounted yet); a scan that cannot run is noted on the download.
func (d DesktopSettings) repair() (DesktopSettings, []string) {
	var problems []string
	if v, ok := canonicalRenewLinks(d.RenewLinks); ok {
		d.RenewLinks = v
	} else {
		problems = append(problems, fmt.Sprintf("renew_links: %q is not auto, ask or off; using auto", d.RenewLinks))
		d.RenewLinks = ""
	}
	if strings.TrimSpace(d.AVScanArgs) == "" {
		d.AVScanArgs = avscan.DefaultArgs
	} else if err := avscan.CheckArgs(d.AVScanArgs); err != nil {
		problems = append(problems, fmt.Sprintf("av_scan_args: %v; using %q", err, avscan.DefaultArgs))
		d.AVScanArgs = avscan.DefaultArgs
	}
	d.AVScanCommand = strings.TrimSpace(d.AVScanCommand)
	if d.AVScanCommand != "" {
		if err := checkCommandSyntax(d.AVScanCommand); err != nil {
			problems = append(problems, fmt.Sprintf("av_scan_command: %v; antivirus scanning is off", err))
			d.AVScanCommand, d.AVScanEnabled = "", false
		} else {
			d.AVScanCommand = filepath.Clean(d.AVScanCommand)
		}
	}
	if d.AVScanEnabled && d.AVScanCommand == "" {
		problems = append(problems, "av_scan_enabled is set without av_scan_command; antivirus scanning is off")
		d.AVScanEnabled = false
	}
	return d, problems
}

// toProto sets the desktop fields (17–21, 27 and 28) of p.
func (d DesktopSettings) toProto(p *downexv1.Settings) {
	p.ClipboardMonitor = d.ClipboardMonitor
	p.MarkDownloadedFiles = d.MarkDownloadedFiles
	p.AvScanEnabled = d.AVScanEnabled
	p.AvScanCommand = d.AVScanCommand
	p.AvScanArgs = d.AVScanArgs
	p.CheckDownloadedFiles = d.CheckFiles
	switch d.RenewMode() {
	case RenewLinksAsk:
		p.RenewLinks = downexv1.RenewLinks_RENEW_LINKS_ASK
	case RenewLinksOff:
		p.RenewLinks = downexv1.RenewLinks_RENEW_LINKS_OFF
	default:
		p.RenewLinks = downexv1.RenewLinks_RENEW_LINKS_AUTO
	}
}

// renewLinksFromProto maps the API value; UNSPECIFIED and AUTO are "" (auto), an
// unknown value is kept as text for normalize to refuse.
func renewLinksFromProto(v downexv1.RenewLinks) string {
	switch v {
	case downexv1.RenewLinks_RENEW_LINKS_UNSPECIFIED, downexv1.RenewLinks_RENEW_LINKS_AUTO:
		return ""
	case downexv1.RenewLinks_RENEW_LINKS_ASK:
		return RenewLinksAsk
	case downexv1.RenewLinks_RENEW_LINKS_OFF:
		return RenewLinksOff
	}
	return fmt.Sprintf("%d", int32(v))
}

// desktopFromProto reads the desktop fields (17–21, 27 and 28) of API settings (a full
// replacement; validation happens in normalize).
func desktopFromProto(p *downexv1.Settings) DesktopSettings {
	return DesktopSettings{
		ClipboardMonitor:    p.GetClipboardMonitor(),
		MarkDownloadedFiles: p.GetMarkDownloadedFiles(),
		AVScanEnabled:       p.GetAvScanEnabled(),
		AVScanCommand:       p.GetAvScanCommand(),
		AVScanArgs:          p.GetAvScanArgs(),
		CheckFiles:          p.GetCheckDownloadedFiles(),
		RenewLinks:          renewLinksFromProto(p.GetRenewLinks()),
	}
}
