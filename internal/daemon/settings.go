package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"downex/internal/engine"
)

// Settings limits and defaults (docs/DAEMON-SPEC.md §3).
const (
	// DefaultMaxConcurrentDownloads is the default number of downloads run at once.
	DefaultMaxConcurrentDownloads = 3
	// MaxConcurrentDownloadsLimit is the largest accepted max_concurrent_downloads.
	MaxConcurrentDownloadsLimit = 16
	// DefaultMaxConnections is the default per-download connection limit.
	DefaultMaxConnections = engine.DefaultMaxConnections
	// MaxConnectionsLimit is the largest accepted connection limit.
	MaxConnectionsLimit = engine.HardMaxConnections
)

// SettingsFileName is the name of the settings file kept next to the database.
const SettingsFileName = "settings.json"

// Settings are the daemon's persisted preferences (docs/DAEMON-SPEC.md §3,
// docs/DESKTOP-SPEC.md §1–5). They are replaced as a whole: Categories is never
// modified in place, so a copy of a Settings value may share it for reading; Clone
// returns an independent copy.
type Settings struct {
	MaxConcurrentDownloads int    `json:"max_concurrent_downloads"`
	GlobalSpeedLimit       int64  `json:"global_speed_limit"` // bytes/s, 0 = unlimited
	DefaultDir             string `json:"default_dir"`        // absolute
	DefaultMaxConnections  int    `json:"default_max_connections"`
	// M4.5. A settings file of an older daemon lacks these; they then take the values of
	// DefaultSettings.
	Categories              []Category `json:"categories"`
	UseCategoryFolders      bool       `json:"use_category_folders"`      // save into the category's folder when Add has no dir
	ConfirmBrowserDownloads bool       `json:"confirm_browser_downloads"` // browser hand-offs await confirmation
	NotifyOnComplete        bool       `json:"notify_on_complete"`
	NotifyOnFailure         bool       `json:"notify_on_failure"`
	LaunchAtLogin           bool       `json:"launch_at_login"` // start downexd when the user signs in
	ShowTrayIcon            bool       `json:"show_tray_icon"`
	// Progress window (docs/DESKTOP-SPEC.md §7). Files of older daemons lack them; they
	// then take the values of DefaultSettings (on).
	ShowProgressWindow bool `json:"show_progress_window"` // open a download's progress window when it starts interactively
	ShowCompleteDialog bool `json:"show_complete_dialog"` // the progress window turns into a completion view
	// M6 (docs/M6-SPEC.md). Each area's type lives in its own file (settings_<area>.go) and is
	// owned by that area; files of older daemons lack them and get the area's defaults.
	Network NetworkSettings `json:"network"` // §2 proxy, host rules, site logins (no passwords)
	Desktop DesktopSettings `json:"desktop"` // §3 clipboard, file marking, antivirus
	Update  UpdateSettings  `json:"update"`  // §4 feed and auto-check
	// Site media (docs/SITE-MEDIA-SPEC.md §3.8): yt-dlp and its tools. Files of older
	// daemons lack it and get DefaultMediaSettings.
	Media MediaSettings `json:"media"`
	// Polite mode (docs/POLITE-SPEC.md §7). Files of older daemons lack it and get
	// DefaultPoliteSettings.
	Polite PoliteSettings `json:"polite"`
}

// DefaultSettings returns the settings of a new installation (and the values of fields
// missing from an older settings file). The numeric fields and DefaultDir are zero:
// normalize replaces them with their defaults and the user's Downloads folder.
func DefaultSettings() Settings {
	return Settings{
		Categories:              DefaultCategories(),
		UseCategoryFolders:      true,
		ConfirmBrowserDownloads: true,
		NotifyOnComplete:        true,
		NotifyOnFailure:         true,
		LaunchAtLogin:           false,
		ShowTrayIcon:            true,
		ShowProgressWindow:      true,
		ShowCompleteDialog:      true,
		Network:                 DefaultNetworkSettings(),
		Desktop:                 DefaultDesktopSettings(),
		Update:                  DefaultUpdateSettings(),
		Media:                   DefaultMediaSettings(),
		Polite:                  DefaultPoliteSettings(),
	}
}

// Clone returns a copy of s that shares no memory with it.
func (s Settings) Clone() Settings {
	s.Categories = cloneCategories(s.Categories)
	s.Network = s.Network.Clone()
	return s
}

// Equal reports whether s and o are the same settings (nil and empty lists are equal).
func (s Settings) Equal(o Settings) bool {
	a, b := s, o
	a.Categories, b.Categories = nil, nil
	a.Network.HostRules, b.Network.HostRules = nil, nil
	a.Network.SiteLogins, b.Network.SiteLogins = nil, nil
	return reflect.DeepEqual(a, b) && slices.EqualFunc(s.Categories, o.Categories, Category.equal) &&
		slices.Equal(s.Network.HostRules, o.Network.HostRules) && slices.Equal(s.Network.SiteLogins, o.Network.SiteLogins)
}

// newSiteLoginID returns a new site login id (docs/M6-SPEC.md §2): 16 random hex digits,
// which never repeat in practice. A login keeps its id for its lifetime; its password is
// stored under it (SetSecret).
func newSiteLoginID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	return hex.EncodeToString(b)
}

// normalize validates s as a full replacement from a client and fills defaults:
// zero max_concurrent_downloads and default_max_connections select their defaults and
// an empty default_dir the user's Downloads folder (from downloadsDir). Categories are
// checked and normalised (see normalizeCategories); the boolean fields are taken as
// they are; the network, desktop and update settings are validated by their areas (new
// site logins get an id), and so are the site-media settings. The returned error
// describes the first invalid field.
func (s Settings) normalize(downloadsDir func() (string, error)) (Settings, error) {
	s, err := s.normalizeBase(downloadsDir)
	if err != nil {
		return s, err
	}
	if s.Categories, err = normalizeCategories(s.Categories, s.DefaultDir); err != nil {
		return s, err
	}
	if s.Network, err = s.Network.Normalize(newSiteLoginID); err != nil {
		return s, err
	}
	if s.Desktop, err = s.Desktop.normalize(); err != nil {
		return s, err
	}
	if s.Update, err = s.Update.normalize(); err != nil {
		return s, err
	}
	if s.Media, err = s.Media.normalize(); err != nil {
		return s, err
	}
	if s.Polite, err = s.Polite.normalize(); err != nil {
		return s, err
	}
	return s, nil
}

// normalizeBase is normalize for the fields of docs/DAEMON-SPEC.md §3.
func (s Settings) normalizeBase(downloadsDir func() (string, error)) (Settings, error) {
	switch {
	case s.MaxConcurrentDownloads == 0:
		s.MaxConcurrentDownloads = DefaultMaxConcurrentDownloads
	case s.MaxConcurrentDownloads < 1 || s.MaxConcurrentDownloads > MaxConcurrentDownloadsLimit:
		return s, fmt.Errorf("max_concurrent_downloads must be between 1 and %d", MaxConcurrentDownloadsLimit)
	}
	if s.GlobalSpeedLimit < 0 {
		return s, errors.New("global_speed_limit must not be negative")
	}
	switch {
	case s.DefaultMaxConnections == 0:
		s.DefaultMaxConnections = DefaultMaxConnections
	case s.DefaultMaxConnections < 1 || s.DefaultMaxConnections > MaxConnectionsLimit:
		return s, fmt.Errorf("default_max_connections must be between 1 and %d", MaxConnectionsLimit)
	}
	if s.DefaultDir == "" {
		dir, err := downloadsDir()
		if err != nil {
			return s, fmt.Errorf("default_dir is empty and the Downloads folder is unknown: %w", err)
		}
		if strings.ContainsRune(dir, 0) || !filepath.IsAbs(dir) {
			return s, errors.New("the Downloads folder is not an absolute path")
		}
		// The system's Downloads folder is trusted even on a network share (folder
		// redirection); a directory from a client is not.
		s.DefaultDir = filepath.Clean(dir)
	} else {
		dir, err := checkDir(s.DefaultDir, "")
		if err != nil {
			// The Downloads folder itself is accepted wherever it is.
			sys, serr := downloadsDir()
			if serr != nil || !filepath.IsAbs(s.DefaultDir) || !samePath(filepath.Clean(s.DefaultDir), filepath.Clean(sys)) {
				return s, fmt.Errorf("default_dir: %w", err)
			}
			dir = filepath.Clean(s.DefaultDir)
		}
		s.DefaultDir = dir
	}
	if fi, err := os.Stat(s.DefaultDir); err == nil && !fi.IsDir() {
		return s, errors.New("default_dir is not a directory")
	}
	return s, nil
}

// repair replaces each invalid field of settings read from disk with its default and
// returns a description of every replaced field. An unknown Downloads folder leaves
// DefaultDir empty (clients must then pass a directory). Invalid categories are
// replaced by DefaultCategories as a whole.
func (s Settings) repair(downloadsDir func() (string, error)) (Settings, []string) {
	var problems []string
	if s.MaxConcurrentDownloads < 0 || s.MaxConcurrentDownloads > MaxConcurrentDownloadsLimit {
		problems = append(problems, fmt.Sprintf("max_concurrent_downloads %d is out of range", s.MaxConcurrentDownloads))
		s.MaxConcurrentDownloads = 0
	}
	if s.GlobalSpeedLimit < 0 {
		problems = append(problems, "global_speed_limit is negative")
		s.GlobalSpeedLimit = 0
	}
	if s.DefaultMaxConnections < 0 || s.DefaultMaxConnections > MaxConnectionsLimit {
		problems = append(problems, fmt.Sprintf("default_max_connections %d is out of range", s.DefaultMaxConnections))
		s.DefaultMaxConnections = 0
	}
	if s.DefaultDir != "" {
		if _, err := (Settings{DefaultDir: s.DefaultDir}).normalizeBase(downloadsDir); err != nil {
			problems = append(problems, err.Error())
			s.DefaultDir = ""
		}
	}
	n, err := s.normalizeBase(downloadsDir)
	if err != nil { // only the Downloads folder lookup can fail here
		problems = append(problems, err.Error())
		n.DefaultDir = ""
	}
	cats, err := normalizeCategories(n.Categories, n.DefaultDir)
	if err != nil {
		problems = append(problems, err.Error()+"; using the default categories")
		cats = DefaultCategories()
	}
	n.Categories = cats
	var more []string
	n.Network, more = repairNetwork(n.Network)
	problems = append(problems, more...)
	n.Desktop, more = n.Desktop.repair()
	problems = append(problems, more...)
	n.Update, more = n.Update.repair()
	problems = append(problems, more...)
	n.Media, more = n.Media.repair()
	problems = append(problems, more...)
	n.Polite, more = n.Polite.repair()
	problems = append(problems, more...)
	return n, problems
}

// repairNetwork replaces invalid network settings read from disk part by part: an
// invalid proxy configuration becomes the system proxy, and each host rule or site login
// that does not validate (or is one too many) is dropped. It describes every replacement.
// User information in proxy URLs (passwords) is removed first (repairUserinfo).
func repairNetwork(n NetworkSettings) (NetworkSettings, []string) {
	var problems []string
	n.Proxy, problems = n.Proxy.repairUserinfo()
	if ok, err := n.Normalize(newSiteLoginID); err == nil {
		return ok, problems
	}
	out := NetworkSettings{Proxy: DefaultNetworkSettings().Proxy}
	if p, err := n.Proxy.normalize(); err != nil {
		problems = append(problems, fmt.Sprintf("proxy: %v; using the system proxy", err))
	} else {
		out.Proxy = p
	}
	for _, r := range n.HostRules {
		try := out
		try.HostRules = append(slices.Clone(out.HostRules), r)
		if v, err := try.Normalize(newSiteLoginID); err != nil {
			problems = append(problems, fmt.Sprintf("%v; the host rule is dropped", err))
		} else {
			out.HostRules = v.HostRules
		}
	}
	for _, l := range n.SiteLogins {
		try := out
		try.SiteLogins = append(slices.Clone(out.SiteLogins), l)
		if v, err := try.Normalize(newSiteLoginID); err != nil {
			problems = append(problems, fmt.Sprintf("%v; the site login is dropped", err))
		} else {
			out.SiteLogins = v.SiteLogins
		}
	}
	return out, problems
}

// settingsFile is the settings file's form: fields added after the first release are
// pointers, so that a file written by an older daemon (which lacks them) is told apart
// from one that sets them to false or empty.
type settingsFile struct {
	MaxConcurrentDownloads  int         `json:"max_concurrent_downloads"`
	GlobalSpeedLimit        int64       `json:"global_speed_limit"`
	DefaultDir              string      `json:"default_dir"`
	DefaultMaxConnections   int         `json:"default_max_connections"`
	Categories              *[]Category `json:"categories"`
	UseCategoryFolders      *bool       `json:"use_category_folders"`
	ConfirmBrowserDownloads *bool       `json:"confirm_browser_downloads"`
	NotifyOnComplete        *bool       `json:"notify_on_complete"`
	NotifyOnFailure         *bool       `json:"notify_on_failure"`
	LaunchAtLogin           *bool       `json:"launch_at_login"`
	ShowTrayIcon            *bool       `json:"show_tray_icon"`
	ShowProgressWindow      *bool       `json:"show_progress_window"`
	ShowCompleteDialog      *bool       `json:"show_complete_dialog"`
	// M6: each area's object; DesktopSettings and UpdateSettings fill what an object lacks
	// with their defaults, and so does MediaSettings (site media).
	Network *NetworkSettings `json:"network"`
	Desktop *DesktopSettings `json:"desktop"`
	Update  *UpdateSettings  `json:"update"`
	Media   *MediaSettings   `json:"media"`
	Polite  *PoliteSettings  `json:"polite"` // Polite mode (docs/POLITE-SPEC.md §7)
}

// loadSettings reads the settings file at path. A missing file (or an empty path)
// yields DefaultSettings, and so does an unreadable one (with the error); fields
// missing from the file (written by an older daemon) take their DefaultSettings values.
func loadSettings(path string) (Settings, error) {
	s := DefaultSettings()
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("read settings: %w", err)
	}
	var f settingsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return s, fmt.Errorf("parse settings %s: %w", path, err)
	}
	s.MaxConcurrentDownloads, s.GlobalSpeedLimit = f.MaxConcurrentDownloads, f.GlobalSpeedLimit
	s.DefaultDir, s.DefaultMaxConnections = f.DefaultDir, f.DefaultMaxConnections
	if f.Categories != nil {
		s.Categories = *f.Categories
	}
	for _, b := range []struct{ dst, src *bool }{
		{&s.UseCategoryFolders, f.UseCategoryFolders},
		{&s.ConfirmBrowserDownloads, f.ConfirmBrowserDownloads},
		{&s.NotifyOnComplete, f.NotifyOnComplete},
		{&s.NotifyOnFailure, f.NotifyOnFailure},
		{&s.LaunchAtLogin, f.LaunchAtLogin},
		{&s.ShowTrayIcon, f.ShowTrayIcon},
		{&s.ShowProgressWindow, f.ShowProgressWindow},
		{&s.ShowCompleteDialog, f.ShowCompleteDialog},
	} {
		if b.src != nil {
			*b.dst = *b.src
		}
	}
	if f.Network != nil {
		s.Network = *f.Network
	}
	if f.Desktop != nil {
		s.Desktop = *f.Desktop
	}
	if f.Update != nil {
		s.Update = *f.Update
	}
	if f.Media != nil {
		s.Media = *f.Media
	}
	if f.Polite != nil {
		s.Polite = *f.Polite
	}
	return s, nil
}

// saveSettings atomically replaces the settings file at path (readable only by the
// current user): it writes a temporary file in the same directory, flushes it and
// renames it over path.
func saveSettings(path string, s Settings) (err error) {
	// Empty lists are written as [] (not null), which a file of an older daemon lacking
	// the field is not.
	s.Categories = cloneCategories(s.Categories)
	if s.Categories == nil {
		s.Categories = []Category{}
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}
	f, err := os.CreateTemp(dir, ".settings-*.tmp") // created with mode 0600
	if err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("write settings: %w", werr)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace settings: %w", err)
	}
	return nil
}
