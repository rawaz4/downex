package platform

import "testing"

func TestUserDir(t *testing.T) {
	const home = "/home/ann"
	tests := []struct {
		name, data, want string
	}{
		{"generated", "# This file is written by xdg-user-dirs-update\nXDG_DESKTOP_DIR=\"$HOME/Desktop\"\nXDG_DOWNLOAD_DIR=\"$HOME/Downloads\"\n", "/home/ann/Downloads"},
		{"absolute", `XDG_DOWNLOAD_DIR="/data/dl"`, "/data/dl"},
		{"braced home", `XDG_DOWNLOAD_DIR="${HOME}/dl"`, "/home/ann/dl"},
		{"home itself", `XDG_DOWNLOAD_DIR="$HOME/"`, "/home/ann"},
		{"escapes", `XDG_DOWNLOAD_DIR="$HOME/a \"b\" \\c \$HOME \x"`, `/home/ann/a "b" \c $HOME \x`},
		{"single quotes", `XDG_DOWNLOAD_DIR='/data/$HOME "q"'`, `/data/$HOME "q"`},
		{"unquoted", `XDG_DOWNLOAD_DIR=$HOME/My\ Files`, "/home/ann/My Files"},
		{"mixed quoting", `XDG_DOWNLOAD_DIR="$HOME"/'x y'/z`, "/home/ann/x y/z"},
		{"export and comment", "  export XDG_DOWNLOAD_DIR=\"$HOME/dl\"   # mine\r\n", "/home/ann/dl"},
		{"utf-8", `XDG_DOWNLOAD_DIR="$HOME/Téléchargements"`, "/home/ann/Téléchargements"},
		{"cleaned", `XDG_DOWNLOAD_DIR="$HOME//dl/./x/../"`, "/home/ann/dl"},
		{"last wins", "XDG_DOWNLOAD_DIR=\"/first\"\nXDG_DOWNLOAD_DIR=\"/second\"\n", "/second"},
		{"invalid later line keeps earlier", "XDG_DOWNLOAD_DIR=\"/first\"\nXDG_DOWNLOAD_DIR=\"$OTHER/x\"\n", "/first"},
		{"absent", `XDG_MUSIC_DIR="$HOME/Music"`, ""},
		{"empty file", "", ""},
		{"commented out", `# XDG_DOWNLOAD_DIR="/data"`, ""},
		{"relative", `XDG_DOWNLOAD_DIR="Downloads"`, ""},
		{"empty value", `XDG_DOWNLOAD_DIR=""`, ""},
		{"other variable", `XDG_DOWNLOAD_DIR="$HOMEDIR/x"`, ""},
		{"other variable braced", `XDG_DOWNLOAD_DIR="${XDG_DATA_HOME}/x"`, ""},
		{"command substitution", "XDG_DOWNLOAD_DIR=\"`id -un`\"", ""},
		{"dollar paren", `XDG_DOWNLOAD_DIR="$(id -un)"`, ""},
		{"unterminated", `XDG_DOWNLOAD_DIR="$HOME/dl`, ""},
		{"two words", `XDG_DOWNLOAD_DIR="$HOME/dl" rm`, ""},
		{"spaces around =", `XDG_DOWNLOAD_DIR = "$HOME/dl"`, ""},
		{"prefix of another key", `XDG_DOWNLOAD_DIR_X="/data"`, ""},
		{"trailing backslash", `XDG_DOWNLOAD_DIR=/data\`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := userDir([]byte(tt.data), "DOWNLOAD", home); got != tt.want {
				t.Fatalf("userDir(%q) = %q, want %q", tt.data, got, tt.want)
			}
		})
	}
}

func TestUserDirWithoutHome(t *testing.T) {
	if got := userDir([]byte(`XDG_DOWNLOAD_DIR="$HOME/dl"`), "DOWNLOAD", ""); got != "" {
		t.Fatalf("userDir without a home = %q, want \"\"", got)
	}
	if got := userDir([]byte(`XDG_DOWNLOAD_DIR="/data/dl"`), "DOWNLOAD", ""); got != "/data/dl" {
		t.Fatalf("userDir without a home = %q, want /data/dl", got)
	}
}
