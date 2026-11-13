package platform

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestToolsDirFor(t *testing.T) {
	cases := []struct {
		goos string
		env  map[string]string
		want string
	}{
		{"windows", map[string]string{"LOCALAPPDATA": `C:\Users\u\AppData\Local`}, `C:\Users\u\AppData\Local\Downex\tools`},
		{"windows", map[string]string{"LOCALAPPDATA": `D:\Local\`}, `D:\Local\Downex\tools`},
		{"windows", map[string]string{"LOCALAPPDATA": `\\server\share\u`}, `\\server\share\u\Downex\tools`},
		{"darwin", map[string]string{"HOME": "/Users/u", "XDG_DATA_HOME": "/elsewhere"}, "/Users/u/Library/Application Support/Downex/tools"},
		{"darwin", map[string]string{"HOME": "/Users/u/"}, "/Users/u/Library/Application Support/Downex/tools"},
		{"linux", map[string]string{"HOME": "/home/u"}, "/home/u/.local/share/downex/tools"},
		{"linux", map[string]string{"HOME": "/home/u", "XDG_DATA_HOME": "/data/u"}, "/data/u/downex/tools"},
		{"linux", map[string]string{"HOME": "/home/u", "XDG_DATA_HOME": "relative/data"}, "/home/u/.local/share/downex/tools"},
		{"freebsd", map[string]string{"HOME": "/home/u"}, "/home/u/.local/share/downex/tools"},
	}
	for _, c := range cases {
		got, err := ToolsDirFor(c.goos, func(k string) string { return c.env[k] })
		if err != nil || got != c.want {
			t.Errorf("ToolsDirFor(%s, %v) = %q, %v, want %q", c.goos, c.env, got, err, c.want)
		}
	}
	bad := []struct {
		goos string
		env  map[string]string
	}{
		{"windows", nil},
		{"windows", map[string]string{"LOCALAPPDATA": `relative\path`}},
		{"windows", map[string]string{"LOCALAPPDATA": `C:`}},
		{"windows", map[string]string{"LOCALAPPDATA": `\\`}},
		{"darwin", nil},
		{"darwin", map[string]string{"HOME": "Users/u"}},
		{"linux", nil},
		{"linux", map[string]string{"HOME": "home/u", "XDG_DATA_HOME": "data"}},
	}
	for _, c := range bad {
		if got, err := ToolsDirFor(c.goos, func(k string) string { return c.env[k] }); err == nil {
			t.Errorf("ToolsDirFor(%s, %v) = %q, want an error", c.goos, c.env, got)
		}
	}
	// Linux needs no home when XDG_DATA_HOME is absolute.
	if got, err := ToolsDirFor("linux", func(k string) string {
		return map[string]string{"XDG_DATA_HOME": "/data/u"}[k]
	}); err != nil || got != "/data/u/downex/tools" {
		t.Errorf("XDG_DATA_HOME without HOME: %q, %v", got, err)
	}
}

func TestToolsDirIsAbsolute(t *testing.T) {
	dir, err := ToolsDir()
	if err != nil {
		t.Skipf("no tools folder in this environment: %v", err)
	}
	if !filepath.IsAbs(dir) {
		t.Errorf("ToolsDir() = %q, not absolute on %s", dir, runtime.GOOS)
	}
}
