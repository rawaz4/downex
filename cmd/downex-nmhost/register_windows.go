//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// registryKeys are the keys (below HKCU or HKLM) where the browsers look for a host.
var registryKeys = map[string]string{
	"chrome":   `Software\Google\Chrome\NativeMessagingHosts\` + hostName,
	"edge":     `Software\Microsoft\Edge\NativeMessagingHosts\` + hostName,
	"chromium": `Software\Chromium\NativeMessagingHosts\` + hostName,
	"brave":    `Software\BraveSoftware\Brave-Browser\NativeMessagingHosts\` + hostName,
	"firefox":  `Software\Mozilla\NativeMessagingHosts\` + hostName,
}

// defaultInstallation writes the manifests to %LOCALAPPDATA%\Downex\nmhost and the keys
// to HKCU; with system, to %ProgramData%\Downex\nmhost and HKLM. Users may create
// directories in %ProgramData%, so the system-wide ones are refused when someone else
// created them, and given a DACL that lets only administrators change them.
func defaultInstallation(system bool) (installation, error) {
	inst := installation{scope: "the current user", reg: winRegistry{key: registry.CURRENT_USER, name: "HKCU"},
		samePath: windowsSamePath}
	var base string
	if system {
		inst.scope, inst.reg = "all users", winRegistry{key: registry.LOCAL_MACHINE, name: "HKLM"}
		dir, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, windows.KF_FLAG_DEFAULT)
		if err != nil {
			return inst, fmt.Errorf("locate the ProgramData folder: %w", err)
		}
		base = dir
		md := manifestDirs{
			base:    dir,
			dirs:    []string{filepath.Join(dir, "Downex"), filepath.Join(dir, "Downex", "nmhost")},
			sddl:    adminManifestSDDL,
			trusted: adminSID,
		}
		inst.protect = md.protect
	} else {
		base = os.Getenv("LOCALAPPDATA")
		if !filepath.IsAbs(base) {
			dir, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, windows.KF_FLAG_DEFAULT)
			if err != nil {
				return inst, fmt.Errorf("locate the local application data folder: %w", err)
			}
			base = dir
		}
	}
	inst.targets = windowsTargets(filepath.Join(base, "Downex", "nmhost"), "")
	return inst, nil
}

// windowsTargets places one manifest per browser in dir and its key below keyPrefix
// (tests use a private prefix).
func windowsTargets(dir, keyPrefix string) map[string]target {
	t := make(map[string]target, len(allBrowsers))
	for _, b := range allBrowsers {
		t[b] = target{browser: b, file: filepath.Join(dir, hostName+"."+b+".json"), regKey: keyPrefix + registryKeys[b]}
	}
	return t
}

// windowsSamePath compares paths case-insensitively after cleaning them.
func windowsSamePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// winRegistry is the registry below one predefined key, in its 64-bit view (which
// 64-bit browsers read first).
type winRegistry struct {
	key  registry.Key
	name string
}

func (r winRegistry) root() string { return r.name }

func (r winRegistry) value(key, name string) (string, bool, error) {
	k, err := registry.OpenKey(r.key, key, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer k.Close()
	v, _, err := k.GetStringValue(name)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (r winRegistry) setValue(key, name, v string) error {
	k, _, err := registry.CreateKey(r.key, key, registry.SET_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(name, v)
}

func (r winRegistry) deleteValue(key, name string) error {
	k, err := registry.OpenKey(r.key, key, registry.SET_VALUE|registry.WOW64_64KEY)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

func (r winRegistry) deleteKey(key string) error {
	if err := registry.DeleteKey(r.key, key); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}
