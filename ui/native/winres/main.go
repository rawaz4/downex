// Command winres writes ui/native's resource object, rsrc_windows_amd64.syso, which go build
// links into downex-ui-native-preview.exe: the window's application manifest
// (packaging/windows/downex-ui.exe.manifest: asInvoker, per-monitor v2 DPI, Common Controls 6,
// Windows 10 and 11, long paths), the icon internal/brand draws (packaging/windows/icon.ico) as
// the executable's first icon, and the version information (the fields of the Wails window's
// ui/build/windows/info.json). build.ps1 runs it; the object is a build output and is not checked in (*.syso is
// ignored).
//
// It is a module of its own so that its one dependency, github.com/tc-hib/winres (the
// library Wails v2 makes these resources with), stays out of Downex's go.mod.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/tc-hib/winres"
	"github.com/tc-hib/winres/version"
)

// iconID is the icon's resource id: ui/native's main.go loads it by this id.
const iconID = 1

func main() {
	out := flag.String("out", "rsrc_windows_amd64.syso", "the object to write")
	manifest := flag.String("manifest", "", "the manifest template (packaging/windows/downex-ui.exe.manifest)")
	icon := flag.String("icon", "", "the icon (.ico)")
	ver := flag.String("version", "0.0.0", "the version: x.y.z for a release, or any text (dev)")
	file := flag.String("file", "downex-ui-native-preview.exe", "the executable's file name")
	desc := flag.String("description", "Downex native window preview", "the file description")
	flag.Parse()
	if err := run(*out, *manifest, *icon, *ver, *file, *desc); err != nil {
		fmt.Fprintln(os.Stderr, "winres:", err)
		os.Exit(1)
	}
}

var (
	versionTextRE = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]*$`)
	leadingRE     = regexp.MustCompile(`^\d+(\.\d+){0,2}`)
)

// numericVersion is the x.y.z form of a version text for the resources' numeric fields
// (the fixed file and product versions, the manifest's assembly version): its leading
// dot-separated numbers, each at most 65535, 0 for the missing ones, so "0.9.2" stays
// "0.9.2", "1.2.3-rc1" is "1.2.3" and "dev" is "0.0.0". The string fields keep the text,
// as scripts/build-windows.ps1's Get-NumericVersion does for the installer's VIVERSION.
func numericVersion(text string) string {
	parts := []string{"0", "0", "0"}
	for i, p := range strings.Split(leadingRE.FindString(text), ".") {
		if p == "" {
			break
		}
		n := strings.TrimLeft(p, "0")
		switch {
		case n == "":
			n = "0"
		case len(n) > 5:
			n = "65535"
		default:
			if v, _ := strconv.Atoi(n); v > 65535 {
				n = "65535"
			}
		}
		parts[i] = n
	}
	return strings.Join(parts, ".")
}

func run(out, manifestPath, iconPath, ver, file, desc string) error {
	if !versionTextRE.MatchString(ver) {
		return fmt.Errorf("version %q: want x.y.z or a version text of letters, digits and .+_-", ver)
	}
	num := numericVersion(ver)
	var rs winres.ResourceSet

	m, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	// The template's one placeholder: the assembly version, x.y.z.0 (numeric only).
	if !bytes.Contains(m, []byte("{{.Info.ProductVersion}}")) {
		return fmt.Errorf("%s: no {{.Info.ProductVersion}} to fill", manifestPath)
	}
	m = bytes.ReplaceAll(m, []byte("{{.Info.ProductVersion}}"), []byte(num))
	if bytes.Contains(m, []byte("{{")) {
		return fmt.Errorf("%s: a template field this tool does not fill", manifestPath)
	}
	for _, must := range []string{`level="asInvoker"`, "Microsoft.Windows.Common-Controls", "permonitorv2", `version="6.0.0.0"`} {
		if !bytes.Contains(m, []byte(must)) {
			return fmt.Errorf("%s: the manifest lacks %s", manifestPath, must)
		}
	}
	if err := rs.Set(winres.RT_MANIFEST, winres.ID(1), winres.LCIDDefault, m); err != nil {
		return err
	}

	f, err := os.Open(iconPath)
	if err != nil {
		return err
	}
	ico, err := winres.LoadICO(f)
	f.Close()
	if err != nil {
		return fmt.Errorf("%s: %w", iconPath, err)
	}
	if err := rs.SetIcon(winres.ID(iconID), ico); err != nil {
		return err
	}

	var vi version.Info
	// SetFileVersion and SetProductVersion fill the fixed (numeric) versions and also write
	// the number as the neutral string table's strings; those strings take the text, as the
	// default table's do (for x.y.z the text is the number, so a release is unchanged).
	vi.SetFileVersion(num)
	vi.SetProductVersion(num)
	for _, k := range []string{version.FileVersion, version.ProductVersion} {
		if err := vi.Set(version.LangNeutral, k, ver); err != nil {
			return err
		}
	}
	for k, v := range map[string]string{
		version.CompanyName:      "Downex",
		version.ProductName:      "Downex",
		version.FileDescription:  desc,
		version.Comments:         "Downex download manager",
		version.InternalName:     strings.TrimSuffix(file, ".exe"),
		version.OriginalFilename: file,
		version.ProductVersion:   ver,
		version.FileVersion:      ver,
	} {
		if err := vi.Set(version.LangDefault, k, v); err != nil {
			return err
		}
	}
	rs.SetVersionInfo(vi)

	var b bytes.Buffer
	if err := rs.WriteObject(&b, winres.ArchAMD64); err != nil {
		return err
	}
	tmp := out + ".tmp"
	if err := os.WriteFile(tmp, b.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, out)
}
