//go:build windows

package avscan

import "testing"

func TestCommandLine(t *testing.T) {
	const file = `C:\Users\A B\Downloads\x & y 100%.zip`
	for _, tc := range []struct{ command, args, want string }{
		{`C:\Program Files\Scanner\scan.exe`, "[File]", `"C:\Program Files\Scanner\scan.exe" "` + file + `"`},
		{`C:\Scanner\scan.exe`, `-Scan -File "[File]" -Quiet`, `C:\Scanner\scan.exe -Scan -File "` + file + `" -Quiet`},
		{`C:\Scanner\scan.exe`, `/target=[file]`, `C:\Scanner\scan.exe /target="` + file + `"`},
		{`C:\Scanner\scan.exe`, `  `, `C:\Scanner\scan.exe`},
		{`C:\Scanner\scan.exe`, `--all`, `C:\Scanner\scan.exe --all`},
	} {
		if got := commandLine(tc.command, tc.args, file); got != tc.want {
			t.Errorf("commandLine(%q, %q) =\n%s\nwant\n%s", tc.command, tc.args, got, tc.want)
		}
	}
}
