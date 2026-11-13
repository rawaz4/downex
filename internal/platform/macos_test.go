package platform

import (
	"encoding/xml"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestDarwinPaths(t *testing.T) {
	got, err := darwinPaths("/Users/ann/")
	if err != nil {
		t.Fatal(err)
	}
	want := darwinDirs{
		downloads:   "/Users/ann/Downloads",
		logs:        "/Users/ann/Library/Logs/Downex",
		launchAgent: "/Users/ann/Library/LaunchAgents/com.downex.agent.plist",
	}
	if got != want {
		t.Fatalf("darwinPaths = %+v, want %+v", got, want)
	}
	for _, home := range []string{"", "Users/ann", `C:\Users\ann`} {
		if d, err := darwinPaths(home); err == nil {
			t.Errorf("darwinPaths(%q) = %+v, want an error", home, d)
		}
	}
}

// plistItems flattens an XML property list into "key:<text>", "string:<text>", "true",
// "false", "[" and "]" items, failing on anything else or on malformed XML.
func plistItems(t *testing.T, data []byte) []string {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(string(data)))
	var items []string
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return items
		}
		if err != nil {
			t.Fatalf("malformed property list: %v\n%s", err, data)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == "array" {
				items = append(items, "]")
			}
			continue
		}
		switch se.Name.Local {
		case "plist":
			if len(se.Attr) != 1 || se.Attr[0].Name.Local != "version" || se.Attr[0].Value != "1.0" {
				t.Fatalf("plist attributes = %v", se.Attr)
			}
		case "dict":
		case "array":
			items = append(items, "[")
		case "true", "false":
			items = append(items, se.Name.Local)
		case "key", "string":
			var text string
			if err := d.DecodeElement(&text, &se); err != nil {
				t.Fatalf("element %s: %v", se.Name.Local, err)
			}
			items = append(items, se.Name.Local+":"+text)
		default:
			t.Fatalf("unexpected element <%s>", se.Name.Local)
		}
	}
}

func TestLaunchAgentPlist(t *testing.T) {
	exe := "/Applications/Downex.app/Contents/Library/LoginItems/Downex Agent.app/Contents/MacOS/downexd-agent"
	got, err := launchAgentPlist(exe, []string{"--background"})
	if err != nil {
		t.Fatal(err)
	}
	want := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.downex.agent</string>
	<key>ProgramArguments</key>
	<array>
		<string>/Applications/Downex.app/Contents/Library/LoginItems/Downex Agent.app/Contents/MacOS/downexd-agent</string>
		<string>--background</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<false/>
	<key>ProcessType</key>
	<string>Interactive</string>
</dict>
</plist>
`
	if string(got) != want {
		t.Fatalf("launchAgentPlist =\n%s\nwant\n%s", got, want)
	}

	// Markup characters are escaped and read back unchanged; non-ASCII text is kept.
	odd := `/Users/ann/Apps/R&D <"beta"> 'x' ]]> Téléchargements/Downex Agent.app/Contents/MacOS/downexd-agent`
	got, err = launchAgentPlist(odd, []string{"--background", "a&b"})
	if err != nil {
		t.Fatal(err)
	}
	wantItems := []string{
		"key:Label", "string:com.downex.agent",
		"key:ProgramArguments", "[", "string:" + odd, "string:--background", "string:a&b", "]",
		"key:RunAtLoad", "true",
		"key:KeepAlive", "false",
		"key:ProcessType", "string:Interactive",
	}
	if items := plistItems(t, got); !slices.Equal(items, wantItems) {
		t.Fatalf("property list items = %q, want %q", items, wantItems)
	}
	if s := string(got); !strings.Contains(s, "R&amp;D &lt;\"beta\"&gt; 'x' ]]&gt;") {
		t.Fatalf("markup not escaped with the predefined entities:\n%s", s)
	}
	if items := plistItems(t, mustPlist(t, exe, nil)); !slices.Contains(items, "string:"+exe) || slices.Contains(items, "string:--background") {
		t.Fatalf("without arguments: %q", items)
	}

	for _, c := range []struct {
		name string
		exe  string
		args []string
	}{
		{"relative", "Downex Agent.app/Contents/MacOS/downexd-agent", nil},
		{"empty", "", nil},
		{"windows path", `C:\Downex\downexd.exe`, nil},
		{"newline", "/x/downexd", []string{"a\nb"}},
		{"tab", "/x/down\texd", nil},
		{"nul", "/x/downexd", []string{"a\x00b"}},
		{"delete", "/x/downexd\x7f", nil},
		{"invalid utf-8", "/x/down\xffexd", nil},
		{"noncharacter", "/x/downexd\uffff", nil},
	} {
		if data, err := launchAgentPlist(c.exe, c.args); err == nil {
			t.Errorf("%s: launchAgentPlist(%q, %q) succeeded:\n%s", c.name, c.exe, c.args, data)
		}
	}
}

func mustPlist(t *testing.T, exe string, args []string) []byte {
	t.Helper()
	data, err := launchAgentPlist(exe, args)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestAppBundle(t *testing.T) {
	for _, c := range []struct {
		exe, bundle string
	}{
		{"/Applications/Downex.app/Contents/MacOS/Downex", "/Applications/Downex.app"},
		{"/Applications/Downex.app/Contents/Helpers/Downex Progress.app/Contents/MacOS/Downex",
			"/Applications/Downex.app/Contents/Helpers/Downex Progress.app"},
		{"/Applications/Downex.app/Contents/Library/LoginItems/Downex Agent.app/Contents/MacOS/downexd-agent",
			"/Applications/Downex.app/Contents/Library/LoginItems/Downex Agent.app"},
		{"/Users/ann/My Apps/Downex.app//Contents/./MacOS/Downex", "/Users/ann/My Apps/Downex.app"},
		{"/Applications/Downex.app/Contents/Helpers/downexd", ""},
		{"/Applications/Downex.app/Contents/MacOS/tools/helper", ""},
		{"/Applications/Downex.app/Contents/MacOS/", ""},
		{"/Applications/Downex.app/Contents/MacOS", ""},
		{"/.app/Contents/MacOS/x", ""},
		{"/usr/local/bin/downex", ""},
		{"Downex.app/Contents/MacOS/Downex", ""},
		{"/Applications/Downex.application/Contents/MacOS/Downex", ""},
		{"", ""},
	} {
		bundle, ok := appBundle(c.exe)
		if bundle != c.bundle || ok != (c.bundle != "") {
			t.Errorf("appBundle(%q) = %q, %v; want %q", c.exe, bundle, ok, c.bundle)
		}
	}
}

func TestOpenArgs(t *testing.T) {
	const bundle = "/Applications/Downex.app/Contents/Helpers/Downex Progress.app"
	// A launch that answers the user is activated; one in the background is not (-g).
	got := openArgs(bundle, false, nil, []string{"--progress", "7"})
	if want := []string{"-n", bundle, "--args", "--progress", "7"}; !slices.Equal(got, want) {
		t.Fatalf("openArgs = %q, want %q", got, want)
	}
	got = openArgs(bundle, true, nil, []string{"--progress", "7"})
	if want := []string{"-n", "-g", bundle, "--args", "--progress", "7"}; !slices.Equal(got, want) {
		t.Fatalf("openArgs(background) = %q, want %q", got, want)
	}
	got = openArgs(bundle, true, []string{"DOWNEX_ENDPOINT=/tmp/a b/rpc.sock", "DOWNEX_DAEMON="}, nil)
	if want := []string{"-n", "-g", "--env", "DOWNEX_ENDPOINT=/tmp/a b/rpc.sock", "--env", "DOWNEX_DAEMON=", bundle}; !slices.Equal(got, want) {
		t.Fatalf("openArgs = %q, want %q", got, want)
	}
	// Arguments that look like options of open(1) stay after --args.
	got = openArgs(bundle, false, nil, []string{"-a", "--env", "X=1"})
	if want := []string{"-n", bundle, "--args", "-a", "--env", "X=1"}; !slices.Equal(got, want) {
		t.Fatalf("openArgs = %q, want %q", got, want)
	}
}

func TestForwardedEnv(t *testing.T) {
	environ := []string{
		"HOME=/Users/ann", "TMPDIR=/var/folders/ab/cd/T/", "DOWNEX_ENDPOINT=/tmp/x.sock", "PATH=/usr/bin",
		"DOWNEX_=odd", "DOWNEX_DAEMON=/opt/downexd", "downex_endpoint=lower", "DOWNEX_E2E=a=b", "NOVALUE",
	}
	want := []string{"DOWNEX_ENDPOINT=/tmp/x.sock", "DOWNEX_DAEMON=/opt/downexd", "DOWNEX_E2E=a=b"}
	if got := forwardedEnv(environ); !slices.Equal(got, want) {
		t.Fatalf("forwardedEnv = %q, want %q", got, want)
	}
	if got := forwardedEnv(nil); len(got) != 0 {
		t.Fatalf("forwardedEnv(nil) = %q", got)
	}
}
