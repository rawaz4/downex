//go:build windows

package sysproxy

import (
	"context"
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modwinhttp  = windows.NewLazySystemDLL("winhttp.dll")
	modkernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procGetIEProxyConfig = modwinhttp.NewProc("WinHttpGetIEProxyConfigForCurrentUser")
	procOpen             = modwinhttp.NewProc("WinHttpOpen")
	procGetProxyForURL   = modwinhttp.NewProc("WinHttpGetProxyForUrl")
	procCloseHandle      = modwinhttp.NewProc("WinHttpCloseHandle")
	procSetTimeouts      = modwinhttp.NewProc("WinHttpSetTimeouts")
	procGlobalFree       = modkernel32.NewProc("GlobalFree")
)

// WinHTTP access types and auto-proxy flags (winhttp.h).
const (
	winhttpAccessTypeNoProxy   = 1
	winhttpAutoproxyAutoDetect = 0x00000001
	winhttpAutoproxyConfigURL  = 0x00000002
	winhttpAutoDetectTypeDHCP  = 0x00000001
	winhttpAutoDetectTypeDNSA  = 0x00000002
)

// Timeouts of the WinHTTP session in milliseconds. They bound fetching an auto-config
// script (WinHTTP's default name-resolution timeout is infinite); the discovery itself
// is bounded by AutoResolver.ResolveProxy.
const (
	winhttpResolveTimeout = 10_000
	winhttpConnectTimeout = 10_000
	winhttpSendTimeout    = 10_000
	winhttpReceiveTimeout = 15_000
)

// ieProxyConfig mirrors WINHTTP_CURRENT_USER_IE_PROXY_CONFIG.
type ieProxyConfig struct {
	fAutoDetect       int32
	lpszAutoConfigURL *uint16
	lpszProxy         *uint16
	lpszProxyBypass   *uint16
}

// autoproxyOptions mirrors WINHTTP_AUTOPROXY_OPTIONS.
type autoproxyOptions struct {
	dwFlags                uint32
	dwAutoDetectFlags      uint32
	lpszAutoConfigURL      *uint16
	lpvReserved            uintptr
	dwReserved             uint32
	fAutoLogonIfChallenged int32
}

// proxyInfo mirrors WINHTTP_PROXY_INFO.
type proxyInfo struct {
	dwAccessType    uint32
	lpszProxy       *uint16
	lpszProxyBypass *uint16
}

// Discover reads the current user's Internet Explorer / WinINET proxy configuration via
// WinHttpGetIEProxyConfigForCurrentUser. It is read-only.
func Discover(context.Context) (Settings, error) {
	var cfg ieProxyConfig
	r, _, err := procGetIEProxyConfig.Call(uintptr(unsafe.Pointer(&cfg)))
	if r == 0 {
		return Settings{}, fmt.Errorf("sysproxy: WinHttpGetIEProxyConfigForCurrentUser: %w", err)
	}
	defer func() {
		globalFree(cfg.lpszAutoConfigURL)
		globalFree(cfg.lpszProxy)
		globalFree(cfg.lpszProxyBypass)
	}()
	s := Settings{AutoDetect: cfg.fAutoDetect != 0}
	s.PACURL = windows.UTF16PtrToString(cfg.lpszAutoConfigURL)
	proxy := windows.UTF16PtrToString(cfg.lpszProxy)
	bypass := windows.UTF16PtrToString(cfg.lpszProxyBypass)
	applyWindowsProxy(&s, proxy)
	s.Bypass = parseWindowsBypass(bypass)
	return s, nil
}

func globalFree(p *uint16) {
	if p != nil {
		_, _, _ = procGlobalFree.Call(uintptr(unsafe.Pointer(p)))
	}
}

// winHTTPSession holds a WinHTTP session handle for per-URL WPAD/PAC resolution.
type winHTTPSession struct {
	mu        sync.Mutex
	handle    uintptr
	configURL string
}

// openSession opens a WinHTTP session for WinHttpGetProxyForUrl. configURL (optional)
// is the IE auto-config URL used alongside auto-detect.
func openSession(configURL string) (*winHTTPSession, error) {
	agent, _ := windows.UTF16PtrFromString("Downex")
	h, _, err := procOpen.Call(
		uintptr(unsafe.Pointer(agent)),
		uintptr(winhttpAccessTypeNoProxy),
		0, 0, 0,
	)
	if h == 0 {
		return nil, fmt.Errorf("sysproxy: WinHttpOpen: %w", err)
	}
	// Best effort: without them WinHTTP's defaults (an infinite name resolution) apply.
	_, _, _ = procSetTimeouts.Call(h, winhttpResolveTimeout, winhttpConnectTimeout, winhttpSendTimeout, winhttpReceiveTimeout)
	return &winHTTPSession{handle: h, configURL: configURL}, nil
}

func (s *winHTTPSession) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handle != 0 {
		_, _, _ = procCloseHandle.Call(s.handle)
		s.handle = 0
	}
}

// proxyForURL resolves the proxies WinHTTP would use for target via WPAD auto-detect
// and, when present, the auto-config URL. It returns the WinINET proxy string (which
// applyWindowsProxy / the daemon map to routes), or "" for direct.
func (s *winHTTPSession) proxyForURL(target string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handle == 0 {
		return "", fmt.Errorf("sysproxy: session closed")
	}
	url16, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return "", err
	}
	opts := autoproxyOptions{
		dwFlags:                winhttpAutoproxyAutoDetect,
		dwAutoDetectFlags:      winhttpAutoDetectTypeDHCP | winhttpAutoDetectTypeDNSA,
		fAutoLogonIfChallenged: 1,
	}
	if s.configURL != "" {
		cfg16, err := windows.UTF16PtrFromString(s.configURL)
		if err == nil {
			opts.dwFlags |= winhttpAutoproxyConfigURL
			opts.lpszAutoConfigURL = cfg16
		}
	}
	var info proxyInfo
	r, _, callErr := procGetProxyForURL.Call(
		s.handle,
		uintptr(unsafe.Pointer(url16)),
		uintptr(unsafe.Pointer(&opts)),
		uintptr(unsafe.Pointer(&info)),
	)
	if r == 0 {
		return "", fmt.Errorf("sysproxy: WinHttpGetProxyForUrl: %w", callErr)
	}
	defer func() {
		globalFree(info.lpszProxy)
		globalFree(info.lpszProxyBypass)
	}()
	return windows.UTF16PtrToString(info.lpszProxy), nil
}
