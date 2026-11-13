package validate

import "slices"

// SettingsFormErrors are the Settings dialog's messages by field ("" and nil = fine).
type SettingsFormErrors struct {
	MaxConcurrent      string
	Speed              string
	DefaultDir         string
	DefaultConnections string
	// Categories is about the list of categories as a whole.
	Categories string
	// CategoryRows are per category, by CategoryDraft.Key.
	CategoryRows map[int]CategoryDraftErrors
	// M6
	Proxy      string
	HTTPProxy  string
	HTTPSProxy string
	SocksProxy string
	Bypass     string
	PacURL     string
	ProxyUser  string
	// HostRuleRows are per host rule, by HostRuleDraft.Key.
	HostRuleRows map[int]HostRuleDraftErrors
	// SiteLoginRows are per site login, by SiteLoginDraft.Key.
	SiteLoginRows map[int]SiteLoginDraftErrors
	AVScanCommand string
	AVScanArgs    string
	UpdateFeedURL string
}

// ErrorKey names a field of SettingsFormErrors as today's window does ("maxConcurrent").
type ErrorKey string

// The fields of SettingsFormErrors, in their order.
const (
	ErrMaxConcurrent      ErrorKey = "maxConcurrent"
	ErrSpeed              ErrorKey = "speed"
	ErrDefaultDir         ErrorKey = "defaultDir"
	ErrDefaultConnections ErrorKey = "defaultConnections"
	ErrCategories         ErrorKey = "categories"
	ErrCategoryRows       ErrorKey = "categoryRows"
	ErrProxy              ErrorKey = "proxy"
	ErrHTTPProxy          ErrorKey = "httpProxy"
	ErrHTTPSProxy         ErrorKey = "httpsProxy"
	ErrSocksProxy         ErrorKey = "socksProxy"
	ErrBypass             ErrorKey = "bypass"
	ErrPacURL             ErrorKey = "pacUrl"
	ErrProxyUser          ErrorKey = "proxyUser"
	ErrHostRuleRows       ErrorKey = "hostRuleRows"
	ErrSiteLoginRows      ErrorKey = "siteLoginRows"
	ErrAVScanCommand      ErrorKey = "avScanCommand"
	ErrAVScanArgs         ErrorKey = "avScanArgs"
	ErrUpdateFeedURL      ErrorKey = "updateFeedUrl"
)

// Keys are the fields that hold an error, in field order.
func (e *SettingsFormErrors) Keys() []ErrorKey {
	var out []ErrorKey
	add := func(set bool, k ErrorKey) {
		if set {
			out = append(out, k)
		}
	}
	add(e.MaxConcurrent != "", ErrMaxConcurrent)
	add(e.Speed != "", ErrSpeed)
	add(e.DefaultDir != "", ErrDefaultDir)
	add(e.DefaultConnections != "", ErrDefaultConnections)
	add(e.Categories != "", ErrCategories)
	add(len(e.CategoryRows) > 0, ErrCategoryRows)
	add(e.Proxy != "", ErrProxy)
	add(e.HTTPProxy != "", ErrHTTPProxy)
	add(e.HTTPSProxy != "", ErrHTTPSProxy)
	add(e.SocksProxy != "", ErrSocksProxy)
	add(e.Bypass != "", ErrBypass)
	add(e.PacURL != "", ErrPacURL)
	add(e.ProxyUser != "", ErrProxyUser)
	add(len(e.HostRuleRows) > 0, ErrHostRuleRows)
	add(len(e.SiteLoginRows) > 0, ErrSiteLoginRows)
	add(e.AVScanCommand != "", ErrAVScanCommand)
	add(e.AVScanArgs != "", ErrAVScanArgs)
	add(e.UpdateFeedURL != "", ErrUpdateFeedURL)
	return out
}

func (e *SettingsFormErrors) count() int { return len(e.Keys()) }

// Empty reports no error.
func (e *SettingsFormErrors) Empty() bool { return e.count() == 0 }

// SettingsPage is a page of the Settings window (DESIGN-SYSTEM.md §4.9).
type SettingsPage string

const (
	PageGeneral     SettingsPage = "general"
	PageDownloads   SettingsPage = "downloads"
	PageNetwork     SettingsPage = "network"
	PageIntegration SettingsPage = "integration"
	PageVideo       SettingsPage = "video"
	PageAppearance  SettingsPage = "appearance"
	PageUpdates     SettingsPage = "updates"
	PageAbout       SettingsPage = "about"
)

// SettingsPages are the pages in the order of the side navigation. Errors are shown on
// Downloads, Network, Integration and Updates (General, Video sites, Appearance and About have
// no field that can be wrong).
var SettingsPages = []SettingsPage{PageGeneral, PageDownloads, PageNetwork, PageIntegration, PageVideo, PageAppearance, PageUpdates, PageAbout}

// SettingsField is a Settings field the window can open with the keyboard on it (the status
// bar's "Custom…" limit).
type SettingsField string

// FieldGlobalSpeedLimit is the global speed limit's field.
const FieldGlobalSpeedLimit SettingsField = "globalSpeedLimit"

var (
	downloadsErrors   = []ErrorKey{ErrMaxConcurrent, ErrSpeed, ErrDefaultDir, ErrDefaultConnections, ErrCategories, ErrCategoryRows}
	networkErrors     = []ErrorKey{ErrProxy, ErrHTTPProxy, ErrHTTPSProxy, ErrSocksProxy, ErrBypass, ErrPacURL, ErrProxyUser, ErrHostRuleRows, ErrSiteLoginRows}
	integrationErrors = []ErrorKey{ErrAVScanCommand, ErrAVScanArgs}
)

// PageOfError is the page that shows the field of an error.
func PageOfError(key ErrorKey) SettingsPage {
	switch {
	case slices.Contains(downloadsErrors, key):
		return PageDownloads
	case slices.Contains(networkErrors, key):
		return PageNetwork
	case slices.Contains(integrationErrors, key):
		return PageIntegration
	}
	return PageUpdates
}

// ErrorsOnPage keeps the errors shown on one page.
func ErrorsOnPage(e SettingsFormErrors, page SettingsPage) SettingsFormErrors {
	keep := func(k ErrorKey) bool { return PageOfError(k) == page }
	var out SettingsFormErrors
	if keep(ErrMaxConcurrent) {
		out.MaxConcurrent, out.Speed, out.DefaultDir, out.DefaultConnections = e.MaxConcurrent, e.Speed, e.DefaultDir, e.DefaultConnections
		out.Categories, out.CategoryRows = e.Categories, e.CategoryRows
	}
	if keep(ErrProxy) {
		out.Proxy, out.HTTPProxy, out.HTTPSProxy, out.SocksProxy = e.Proxy, e.HTTPProxy, e.HTTPSProxy, e.SocksProxy
		out.Bypass, out.PacURL, out.ProxyUser = e.Bypass, e.PacURL, e.ProxyUser
		out.HostRuleRows, out.SiteLoginRows = e.HostRuleRows, e.SiteLoginRows
	}
	if keep(ErrAVScanCommand) {
		out.AVScanCommand, out.AVScanArgs = e.AVScanCommand, e.AVScanArgs
	}
	if keep(ErrUpdateFeedURL) {
		out.UpdateFeedURL = e.UpdateFeedURL
	}
	return out
}

// FirstErrorPage is the first page (in the order of the side navigation) that has an error; ok
// is false when there is none.
func FirstErrorPage(e SettingsFormErrors) (page SettingsPage, ok bool) {
	pages := map[SettingsPage]bool{}
	for _, k := range e.Keys() {
		pages[PageOfError(k)] = true
	}
	for _, p := range SettingsPages {
		if pages[p] {
			return p, true
		}
	}
	return "", false
}
