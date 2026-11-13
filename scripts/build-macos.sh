#!/usr/bin/env bash
# Builds Downex for macOS (docs/MACOS-SPEC.md §1 and §6): one universal (arm64 + x86_64)
# Downex.app and the disk image Downex-<version>.dmg. Run it on a Mac; see usage() for the
# prerequisites. The bundle is assembled from the templates in packaging/macos/.
#
# Written for the bash 3.2 that macOS ships as /bin/bash: only indexed arrays, never expanded
# while empty (an error under set -u before bash 4.4).
set -euo pipefail

# (cd prints the directory when $CDPATH finds it: hence >/dev/null.)
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." >/dev/null && pwd -P)"
PKG="$ROOT/packaging/macos"
MIN_MACOS=13.0 # LSMinimumSystemVersion of every bundle and the deployment target of the cgo builds

usage() {
	cat <<'EOF'
Usage: scripts/build-macos.sh [-v VERSION] [-o DIR] [-w] [-r]

Builds, on a Mac, into DIR (default: dist/macos under the repository root):
  Downex.app              universal (arm64 + x86_64) app bundle, signed:
    Contents/MacOS/Downex                       the window: the native window (ui/native over
                                                ui/mac, go build with cgo), or with -w today's
                                                Wails window (wails build -platform darwin/universal)
    Contents/Helpers/downexd, downex, downex-nmhost       daemon, CLI, native-messaging relay
    Contents/Helpers/Downex Progress.app        the window binary as a Dock-less helper app
    Contents/Library/LoginItems/Downex Agent.app  downexd built with cgo (menu-bar icon,
                                                notifications)
    Contents/Resources/extension/chrome-mv3, firefox-mv3, EXTENSION.txt, AppIcon.icns
  Downex-VERSION.dmg      the app, a link to /Applications and "Read Me.txt"

Options:
  -v VERSION   version reported by downexd, downex, downex-nmhost and the window (-X
               main.version; downex --version) and used in the bundle versions and the disk
               image name. Default: dev. The browser extension's builds get it as
               DOWNEX_VERSION (ext/wxt.config.ts): a release version such as 1.0.0 for both; for
               a pre-release such as 1.0.0-rc.1 the whole tag for Chrome and Edge (its
               version_name) and only 1.0.0 for Firefox, whose build refuses a pre-release; a
               version that does not start with a manifest version (dev) leaves package.json's.
  -o DIR       output directory. Default: dist/macos.
  -w           build the window with Wails and WebKit (ui/frontend), as before the native window.
  -r           a build to publish: refuses to start unless VERSION is a semantic version without
               a leading "v" (x.y.z with an optional -pre-release, the rule of internal/update)
               and the update key and feed are known, so that the build can update itself. They
               come from packaging/release-update.txt (DOWNEX_RELEASE_UPDATE_FILE names another
               file, or none); a file without both, and DOWNEX_UPDATE_PUBLIC_KEY or
               DOWNEX_UPDATE_FEED_URL that differ from that file, stop the build unless
               DOWNEX_RELEASE_USE_ENV_UPDATE=1 is set. downexd,
               downex and downex-nmhost are built without their symbol tables and debug
               information (-s -w), and every Go program without version-control stamps
               (-buildvcs=false). Use it for every published DMG.
  -h           show this help.

Environment:
  DEVELOPER_ID     code-signing identity ("Developer ID Application: Name (TEAMID)" or its SHA-1
                   hash, see: security find-identity -v -p codesigning). Unset: ad-hoc signing
                   (codesign -s -), which runs on this Mac; elsewhere Gatekeeper blocks it until
                   the user approves it (right-click -> Open, see README.md).
  NOTARY_PROFILE   notarytool keychain profile (xcrun notarytool store-credentials NAME ...).
                   Set (needs DEVELOPER_ID): the app and the disk image are notarized and stapled.
  DOWNEX_UPDATE_PUBLIC_KEY  base64 ed25519 public key of the update manifests (go run
                   ./scripts/release-sign keygen), compiled into downexd and the agent. Unset: the
                   build cannot check for updates.
  DOWNEX_UPDATE_FEED_URL  https URL of the release feed's signed manifest that new installations
                   check, compiled into downexd and the agent. Unset: a new installation has no
                   update feed until the user sets one.
                   The script prints the key and feed it compiles in and where they came from.
  DOWNEX_RELEASE_USE_ENV_UPDATE  1: with -r, compile in DOWNEX_UPDATE_PUBLIC_KEY and
                   DOWNEX_UPDATE_FEED_URL even when they differ from packaging/release-update.txt
                   or that file does not give them (a test feed, or a new key on purpose). Installations built with another key
                   cannot take this one's updates.

Needs: macOS with the Xcode Command Line Tools (xcode-select --install), Go 1.27 or later,
Node.js 24 with pnpm (the browser extension), and for -w the Wails CLI v2
(go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.0, with $(go env GOPATH)/bin on PATH).
EOF
}

die() {
	printf 'build-macos.sh: %s\n' "$*" >&2
	exit 1
}

step() {
	printf '==> %s\n' "$*"
}

# run prints a command, then runs it; set -e stops the script when it fails.
run() {
	printf '    %s\n' "$(printf '%q ' "$@")"
	"$@"
}

require() {
	command -v "$1" >/dev/null 2>&1 || die "$1 was not found on PATH: $2"
}

parse_args() {
	VERSION=dev
	OUT="$ROOT/dist/macos"
	WEBVIEW=0
	RELEASE=0
	if [[ ${1:-} == --help ]]; then
		usage
		exit 0
	fi
	local opt
	OPTIND=1
	while getopts ':v:o:wrh' opt; do
		case $opt in
		v) VERSION=$OPTARG ;;
		o) OUT=$OPTARG ;;
		w) WEBVIEW=1 ;;
		r) RELEASE=1 ;;
		h)
			usage
			exit 0
			;;
		:) die "option -$OPTARG needs a value (see -h)" ;;
		*) die "unknown option -$OPTARG (see -h)" ;;
		esac
	done
	shift $((OPTIND - 1))
	if [[ $# -gt 0 ]]; then
		die "unexpected argument '$1' (see -h)"
	fi
	# The same rule as scripts/build-windows.ps1; it also keeps the version safe in the sed
	# replacements of render_template and in file names.
	local re='^[0-9A-Za-z][0-9A-Za-z.+_-]*$'
	if ! [[ $VERSION =~ $re ]]; then
		die "invalid version '$VERSION': letters, digits and . + _ - only, starting with a letter or digit"
	fi
	if [[ -z $OUT ]]; then
		die "the output directory (-o) is empty"
	fi
	KEY_FROM=DOWNEX_UPDATE_PUBLIC_KEY
	FEED_FROM=DOWNEX_UPDATE_FEED_URL
	if [[ $RELEASE == 1 ]]; then
		# The release's key and feed live in packaging/release-update.txt.
		# DOWNEX_RELEASE_UPDATE_FILE names another file, or "none" for none (tests)
		local rel="${DOWNEX_RELEASE_UPDATE_FILE:-$ROOT/packaging/release-update.txt}" line file_key="" file_feed=""
		if [[ $rel != none && -f $rel ]]; then
			while IFS= read -r line || [[ -n $line ]]; do
				line=${line%$'\r'}
				case $line in
				public_key=*) file_key=${line#public_key=} ;;
				feed_url=*) file_feed=${line#feed_url=} ;;
				esac
			done <"$rel"
		fi
		# The environment overrides the file only on purpose (DOWNEX_RELEASE_USE_ENV_UPDATE=1): a
		# key or feed left over from a test would cut every installation of this release off
		# from the real updates. Without it the file must give both, so that a file that is
		# missing, unreadable or "none" cannot let such a key or feed through unchecked.
		if [[ ${DOWNEX_RELEASE_USE_ENV_UPDATE:-} != 1 ]]; then
			if [[ -z $file_key || -z $file_feed ]]; then
				die "-r needs public_key and feed_url in $rel: restore that file, or set DOWNEX_RELEASE_USE_ENV_UPDATE=1 to build this release with DOWNEX_UPDATE_PUBLIC_KEY and DOWNEX_UPDATE_FEED_URL"
			fi
			if [[ -n ${DOWNEX_UPDATE_PUBLIC_KEY:-} && -n $file_key && $DOWNEX_UPDATE_PUBLIC_KEY != "$file_key" ]]; then
				die "DOWNEX_UPDATE_PUBLIC_KEY ($DOWNEX_UPDATE_PUBLIC_KEY) differs from public_key in $rel ($file_key): unset it, or set DOWNEX_RELEASE_USE_ENV_UPDATE=1 to build this release with it"
			fi
			if [[ -n ${DOWNEX_UPDATE_FEED_URL:-} && -n $file_feed && $DOWNEX_UPDATE_FEED_URL != "$file_feed" ]]; then
				die "DOWNEX_UPDATE_FEED_URL ($DOWNEX_UPDATE_FEED_URL) differs from feed_url in $rel ($file_feed): unset it, or set DOWNEX_RELEASE_USE_ENV_UPDATE=1 to build this release with it"
			fi
		fi
		if [[ -z ${DOWNEX_UPDATE_PUBLIC_KEY:-} && -n $file_key ]]; then
			export DOWNEX_UPDATE_PUBLIC_KEY=$file_key
			KEY_FROM=$rel
		fi
		if [[ -z ${DOWNEX_UPDATE_FEED_URL:-} && -n $file_feed ]]; then
			export DOWNEX_UPDATE_FEED_URL=$file_feed
			FEED_FROM=$rel
		fi
	fi
	check_update_env
	if [[ $RELEASE == 1 ]]; then
		check_release
	fi
	case $OUT in
	/*) ;;
	*) OUT="$PWD/$OUT" ;;
	esac
}

# The rules of a release (scripts/build-windows.ps1 has the same): a semantic version as
# internal/update.ParseVersion reads it (x.y.z, an optional "v" before it, -pre-release and +build
# identifiers of letters, digits and hyphens), and an https feed URL plain enough to pass through
# -ldflags unquoted (the daemon checks it again).
SEMVER_RE='^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'
FEED_RE='^https://[A-Za-z0-9.-]+(:[0-9]+)?(/[A-Za-z0-9._~/?&=%+-]*)?$'

# check_update_env checks the update key and feed when they are set.
check_update_env() {
	if [[ -n ${DOWNEX_UPDATE_PUBLIC_KEY:-} ]] && ! [[ $DOWNEX_UPDATE_PUBLIC_KEY =~ ^[A-Za-z0-9+/]{43}=$ ]]; then
		die "DOWNEX_UPDATE_PUBLIC_KEY is not a base64 ed25519 public key"
	fi
	if [[ -n ${DOWNEX_UPDATE_FEED_URL:-} ]] && ! [[ $DOWNEX_UPDATE_FEED_URL =~ $FEED_RE ]]; then
		die "DOWNEX_UPDATE_FEED_URL is not an https URL of letters, digits and ._~/?&=%+-: '$DOWNEX_UPDATE_FEED_URL'"
	fi
}

# check_release refuses a release (-r) that could not update itself: a published build without
# them strands every installation on it.
check_release() {
	if ! [[ $VERSION =~ $SEMVER_RE ]]; then
		die "-r needs -v to be a semantic version such as 1.2.3 (not '$VERSION')"
	fi
	if [[ $VERSION == v* ]]; then
		die "-r needs -v without a leading 'v' (${VERSION#v}, not $VERSION): it names the disk image and the release that the update feed describes"
	fi
	if [[ -z ${DOWNEX_UPDATE_PUBLIC_KEY:-} ]]; then
		die "-r needs DOWNEX_UPDATE_PUBLIC_KEY (the update signing public key)"
	fi
	if [[ -z ${DOWNEX_UPDATE_FEED_URL:-} ]]; then
		die "-r needs DOWNEX_UPDATE_FEED_URL (the https URL of the update manifest)"
	fi
}

check_host() {
	local os
	os=$(uname -s)
	if [[ $os != Darwin ]]; then
		die "this script builds the macOS app and runs on macOS only (this is $os). From Windows or Linux only the pure-Go parts can be compile-checked: GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/... ./internal/..."
	fi
}

check_tools() {
	if ! xcode-select -p >/dev/null 2>&1; then
		die "the Xcode Command Line Tools are not installed: run xcode-select --install"
	fi
	require go "install Go 1.27 or later (https://go.dev/dl/)"
	require node "install Node.js 24 (https://nodejs.org/)"
	require pnpm "install pnpm (corepack enable pnpm, or npm install -g pnpm)"
	if [[ $WEBVIEW == 1 ]]; then
		require wails "go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.0 and put \$(go env GOPATH)/bin on PATH"
	fi
	local tool
	for tool in clang lipo otool codesign hdiutil ditto sips iconutil plutil xattr; do
		require "$tool" "it is part of macOS and the Xcode Command Line Tools"
	done

	local gov nodev wv go_re='^go1\.([0-9]+)' node_re='^v([0-9]+)'
	gov=$(go env GOVERSION)
	if [[ $gov =~ $go_re ]] && [[ ${BASH_REMATCH[1]} -lt 27 ]]; then
		die "Go 1.27 or later is needed (found $gov)"
	fi
	nodev=$(node --version)
	if [[ $nodev =~ $node_re ]] && [[ ${BASH_REMATCH[1]} -lt 22 ]]; then
		die "Node.js 22 or later is needed, 24 recommended (found $nodev)"
	fi
	if [[ $WEBVIEW == 1 ]]; then
		wv=$(wails version 2>&1) || true
		if [[ $wv != *v2.* ]]; then
			die "the Wails CLI v2 is needed (wails version printed: $wv)"
		fi
	fi

	if [[ -n ${DEVELOPER_ID:-} ]]; then
		require security "it is part of macOS"
		local ids
		ids=$(security find-identity -v -p codesigning) || die "security find-identity failed"
		if ! grep -Fq -- "$DEVELOPER_ID" <<<"$ids"; then
			die "DEVELOPER_ID '$DEVELOPER_ID' is not a valid code-signing identity in the keychain (security find-identity -v -p codesigning lists them)"
		fi
		IDENTITY=$DEVELOPER_ID
	else
		IDENTITY=-
	fi
	if [[ -n ${NOTARY_PROFILE:-} ]]; then
		if [[ $IDENTITY == - ]]; then
			die "NOTARY_PROFILE needs DEVELOPER_ID: Apple notarizes only Developer ID signed code"
		fi
		require xcrun "it is part of the Xcode Command Line Tools"
		require spctl "it is part of macOS"
		xcrun --find notarytool >/dev/null 2>&1 || die "xcrun cannot find notarytool: update the Xcode Command Line Tools"
		xcrun --find stapler >/dev/null 2>&1 || die "xcrun cannot find stapler: update the Xcode Command Line Tools"
	fi
}

# read_extension_ids reads FILE (default: ext/EXTENSION_ID.txt): one Chromium extension ID per
# line, # starts a comment, blank lines are ignored, a repeated ID counts once. EXTENSION_IDS is the
# comma-separated list that downex-nmhost allows by default; EXTENSION_ID is the first, the ID of
# the extension folder in the app (EXTENSION.txt shows it).
read_extension_ids() {
	local file=${1:-$ROOT/ext/EXTENSION_ID.txt} line id re='^[a-p]{32}$'
	[[ -f $file ]] || die "$file is missing"
	EXTENSION_ID=""
	EXTENSION_IDS=""
	while IFS= read -r line || [[ -n $line ]]; do
		line=${line%%#*}
		id=${line#"${line%%[![:space:]]*}"}
		id=${id%"${id##*[![:space:]]}"}
		if [[ -z $id ]]; then
			continue
		fi
		if ! [[ $id =~ $re ]]; then
			die "ext/EXTENSION_ID.txt: '$id' is not a Chromium extension ID"
		fi
		case ",$EXTENSION_IDS," in
		*",$id,"*) ;;
		,,)
			EXTENSION_ID=$id
			EXTENSION_IDS=$id
			;;
		*) EXTENSION_IDS="$EXTENSION_IDS,$id" ;;
		esac
	done <"$file"
	if [[ -z $EXTENSION_IDS ]]; then
		die "ext/EXTENSION_ID.txt holds no Chromium extension ID"
	fi
}

# version_number prints the decimal digits $1 without leading zeros (0 when nothing is left).
version_number() {
	local n=${1#"${1%%[!0]*}"}
	if [[ -z $n ]]; then
		n=0
	fi
	printf '%s' "$n"
}

# bundle_version prints the x.y.z form of version $1 for CFBundleShortVersionString and
# CFBundleVersion, which must be period-separated integers: the version's leading dot-separated
# numbers, 0 for the missing ones ("dev" -> 0.0.0, "0.4.0-beta.1" -> 0.4.0, "1.2" -> 1.2.0).
bundle_version() {
	local re='^([0-9]+)(\.([0-9]+))?(\.([0-9]+))?' a=0 b=0 c=0
	if [[ $1 =~ $re ]]; then
		a=${BASH_REMATCH[1]}
		b=${BASH_REMATCH[3]:-0}
		c=${BASH_REMATCH[5]:-0}
	fi
	printf '%s.%s.%s\n' "$(version_number "$a")" "$(version_number "$b")" "$(version_number "$c")"
}

# render_template SRC DEST copies the template SRC to DEST with @VERSION@, @BUNDLE_VERSION@ and
# @EXTENSION_ID@ replaced, and fails (removing DEST) when another @NAME@ placeholder is left.
render_template() {
	local src=$1 dest=$2 left
	[[ -f $src ]] || die "template $src is missing"
	sed -e "s|@VERSION@|$VERSION|g" \
		-e "s|@BUNDLE_VERSION@|$BUNDLE_VERSION|g" \
		-e "s|@EXTENSION_ID@|$EXTENSION_ID|g" "$src" >"$dest"
	if grep -Eq '@[A-Z_]+@' "$dest"; then
		left=$(grep -Eo '@[A-Z_]+@' "$dest" | sort -u | tr '\n' ' ') || true
		rm -f "$dest"
		die "$src has placeholders this script does not fill: $left"
	fi
}

# render_plist is render_template for a property list, checked with plutil.
render_plist() {
	render_template "$1" "$2"
	plutil -lint -s "$2" || die "$2 (from $1) is not a valid property list"
}

check_universal() {
	if ! lipo "$1" -verify_arch arm64 x86_64; then
		die "$1 is not a universal arm64 + x86_64 binary ($(lipo -info "$1" 2>&1))"
	fi
}

# go_universal NAME PACKAGE CGO LDFLAGS builds PACKAGE for arm64 and amd64 with CGO_ENABLED=CGO
# and joins the two into the universal binary $WORK/universal/NAME. Go passes -arch to clang
# itself, so the cgo build of the other architecture needs no cross compiler.
go_universal() {
	local name=$1 pkg=$2 cgo=$3 ldflags=$4 arch
	local -a vars=(GOOS=darwin "CGO_ENABLED=$cgo")
	if [[ $cgo == 1 ]]; then
		vars+=("CGO_CFLAGS=${CGO_CFLAGS:--O2 -g} -mmacosx-version-min=$MIN_MACOS"
			"CGO_LDFLAGS=${CGO_LDFLAGS:--O2 -g} -mmacosx-version-min=$MIN_MACOS")
	fi
	for arch in arm64 amd64; do
		mkdir -p "$WORK/$arch"
		run env "${vars[@]}" "GOARCH=$arch" go build -trimpath -ldflags "$ldflags" -o "$WORK/$arch/$name" "$pkg"
	done
	mkdir -p "$WORK/universal"
	run lipo -create -output "$WORK/universal/$name" "$WORK/arm64/$name" "$WORK/amd64/$name"
	check_universal "$WORK/universal/$name"
}

# strip_ldflags prints "-s -w " for a release (-r): no symbol table and DWARF (panic traces keep
# their function names), nothing otherwise.
strip_ldflags() {
	if [[ ${RELEASE:-0} == 1 ]]; then
		printf '%s' "-s -w "
	fi
}

# daemon_ldflags prints the -ldflags of downexd and the agent: the version and, when they are
# set, the update signing key and the default update feed (docs/M6-SPEC.md §4).
daemon_ldflags() {
	local flags
	flags="$(strip_ldflags)-X main.version=$VERSION"
	check_update_env
	if [[ -n ${DOWNEX_UPDATE_PUBLIC_KEY:-} ]]; then
		flags="$flags -X downex/internal/update.PublicKey=$DOWNEX_UPDATE_PUBLIC_KEY"
	fi
	if [[ -n ${DOWNEX_UPDATE_FEED_URL:-} ]]; then
		flags="$flags -X downex/internal/update.DefaultFeedURL=$DOWNEX_UPDATE_FEED_URL"
	fi
	printf '%s' "$flags"
}

build_go() {
	local dflags strip
	dflags=$(daemon_ldflags) || exit 1
	strip=$(strip_ldflags)
	if [[ -n ${DOWNEX_UPDATE_PUBLIC_KEY:-} ]]; then
		printf '    update public key: %s (from %s)\n' "$DOWNEX_UPDATE_PUBLIC_KEY" "${KEY_FROM:-DOWNEX_UPDATE_PUBLIC_KEY}"
	else
		printf 'note: DOWNEX_UPDATE_PUBLIC_KEY is not set: this build cannot check for updates\n' >&2
	fi
	if [[ -n ${DOWNEX_UPDATE_FEED_URL:-} ]]; then
		printf '    update feed: %s (from %s)\n' "$DOWNEX_UPDATE_FEED_URL" "${FEED_FROM:-DOWNEX_UPDATE_FEED_URL}"
	else
		printf 'note: DOWNEX_UPDATE_FEED_URL is not set: new installations have no update feed\n' >&2
	fi
	step "downexd, downex and downex-nmhost ($VERSION, darwin/arm64 + darwin/amd64, CGO_ENABLED=0)"
	go_universal downexd ./cmd/downexd 0 "$dflags"
	go_universal downex ./cmd/downex 0 "${strip}-X main.version=$VERSION"
	go_universal downex-nmhost ./cmd/downex-nmhost 0 "${strip}-X main.version=$VERSION -X main.DefaultChromeExtensionIDs=$EXTENSION_IDS"

	step "Downex Agent: downexd with cgo and Cocoa (menu-bar icon, notifications)"
	go_universal downexd-agent ./cmd/downexd 1 "$dflags"
	local libs
	libs=$(otool -L "$WORK/universal/downexd-agent")
	if ! grep -Eq '(AppKit|Cocoa)\.framework' <<<"$libs"; then
		printf 'warning: downexd-agent does not link AppKit: it was built without the menu-bar icon (internal/tray, darwin && cgo)\n' >&2
	fi
}

# extension_versions sets EXT_CHROMIUM_VERSION and EXT_FIREFOX_VERSION, the DOWNEX_VERSION of the
# extension's builds for version $1, read as ext/wxt.config.ts releaseVersion reads it (the same
# rule as Get-ExtensionVersion in scripts/build-windows.ps1): the leading digits and dots (a "v"
# before them and the final dot dropped) are the manifest version, 1 to 4 numbers up to 65535
# without leading zeros. Chromium takes the whole text (a pre-release becomes version_name);
# Firefox refuses a pre-release, so it takes the numbers only. Both are empty for a text without
# a manifest version (dev), which leaves package.json's version.
extension_versions() {
	local num part re='^(0|[1-9][0-9]{0,4})(\.(0|[1-9][0-9]{0,4})){0,3}$'
	num=${1#v}
	num=${num%%[!0-9.]*}
	num=${num%.}
	EXT_CHROMIUM_VERSION=""
	EXT_FIREFOX_VERSION=""
	if ! [[ $num =~ $re ]]; then
		return 0
	fi
	for part in ${num//./ }; do
		if [[ $part -gt 65535 ]]; then
			return 0
		fi
	done
	EXT_CHROMIUM_VERSION=$1
	EXT_FIREFOX_VERSION=$num
}

# ext_build VERSION SCRIPT runs "pnpm SCRIPT" in ext/ with DOWNEX_VERSION=VERSION, or without
# DOWNEX_VERSION when VERSION is empty (never one left in the environment).
ext_build() {
	if [[ -n $1 ]]; then
		run env "DOWNEX_VERSION=$1" pnpm "$2"
	else
		run env -u DOWNEX_VERSION pnpm "$2"
	fi
}

build_extension() {
	step "browser extension (Chrome, Edge, Brave, Chromium and Firefox)"
	extension_versions "$VERSION"
	if [[ -z $EXT_CHROMIUM_VERSION ]]; then
		printf "note: %s is not a manifest version: the extension keeps package.json's version\n" "$VERSION" >&2
	elif [[ $EXT_FIREFOX_VERSION != "${VERSION#v}" ]]; then
		printf 'note: Firefox takes no pre-release: its manifest version is %s\n' "$EXT_FIREFOX_VERSION" >&2
	fi
	(
		cd "$ROOT/ext"
		run pnpm install --frozen-lockfile
		ext_build "$EXT_CHROMIUM_VERSION" build
		ext_build "$EXT_FIREFOX_VERSION" build:firefox
	)
	local target
	for target in chrome-mv3 firefox-mv3; do
		[[ -f "$EXT_OUT/$target/manifest.json" ]] || die "the extension build made no $EXT_OUT/$target"
	done
}

# window_ldflags prints the -ldflags of the native window: the version and today's
# single-instance id, so that it takes today's locks and files (ui/native/glue.lockID).
window_ldflags() {
	printf '%s' "-X main.version=$VERSION -X downex/ui/native/glue.lockID=downex-ui-6f0d2c1e-4b7a-4e58-9a3d-2c8b7e51f904"
}

# build_window builds the window binary into $WORK/window/Downex: the native window by default,
# today's Wails window with -w.
build_window() {
	if [[ $WEBVIEW == 1 ]]; then
		build_window_webview
		return
	fi
	step "desktop window (native: ui/native over ui/mac, darwin/arm64 + darwin/amd64, cgo)"
	local arch
	# Not next to the CLI in $WORK/universal: Downex and downex are one file name on a
	# case-insensitive volume, which is how macOS formats its disks by default.
	for arch in arm64 amd64; do
		mkdir -p "$WORK/window-$arch"
		run env GOOS=darwin CGO_ENABLED=1 "GOARCH=$arch" \
			"CGO_CFLAGS=${CGO_CFLAGS:--O2 -g} -mmacosx-version-min=$MIN_MACOS" \
			"CGO_LDFLAGS=${CGO_LDFLAGS:--O2 -g} -mmacosx-version-min=$MIN_MACOS" \
			go build -trimpath -ldflags "$(window_ldflags)" -o "$WORK/window-$arch/Downex" ./ui/native
	done
	mkdir -p "$WORK/window"
	run lipo -create -output "$WORK/window/Downex" "$WORK/window-arm64/Downex" "$WORK/window-amd64/Downex"
	check_universal "$WORK/window/Downex"
	local libs
	libs=$(otool -L "$WORK/window/Downex")
	if ! grep -Eq '(AppKit|Cocoa)\.framework' <<<"$libs"; then
		die "the window does not link AppKit: it was built without cgo (ui/mac needs darwin && cgo)"
	fi
}

build_window_webview() {
	step "desktop window (wails build -platform darwin/universal)"
	(
		cd "$ROOT/ui/frontend"
		run pnpm install --frozen-lockfile
	)
	(
		cd "$ROOT/ui"
		# -m and -nosyncgomod: never rewrite go.mod during a build. Wails builds both architectures
		# with cgo and joins them with lipo; it keeps a -mmacosx-version-min it finds in the flags.
		run env "CGO_CFLAGS=${CGO_CFLAGS:--O2 -g} -mmacosx-version-min=$MIN_MACOS" \
			"CGO_LDFLAGS=${CGO_LDFLAGS:--O2 -g} -mmacosx-version-min=$MIN_MACOS" \
			wails build -clean -trimpath -m -nosyncgomod -platform darwin/universal -ldflags "-X main.version=$VERSION"
	)
	# Only the binary is taken: the bundle around it is assembled from packaging/macos (Wails'
	# own Info.plist from ui/build/darwin is not used).
	local bin="$ROOT/ui/build/bin/Downex.app/Contents/MacOS/Downex"
	[[ -f $bin ]] || die "wails build made no $bin"
	check_universal "$bin"
	# Not next to the CLI in $WORK/universal: Downex and downex are one file name on a
	# case-insensitive volume, which is how macOS formats its disks by default.
	mkdir -p "$WORK/window"
	run ditto "$bin" "$WORK/window/Downex"
}

# make_icns writes $WORK/AppIcon.icns from ui/build/appicon.png (1024 x 1024).
make_icns() {
	step "icon"
	local png="$ROOT/ui/build/appicon.png" set="$WORK/AppIcon.iconset" s
	[[ -f $png ]] || die "$png is missing (go run ./ui/build/genicon.go makes it)"
	rm -rf "$set"
	mkdir -p "$set"
	for s in 16 32 128 256 512; do
		sips -z "$s" "$s" "$png" --out "$set/icon_${s}x${s}.png" >/dev/null
		sips -z "$((s * 2))" "$((s * 2))" "$png" --out "$set/icon_${s}x${s}@2x.png" >/dev/null
	done
	run iconutil -c icns -o "$WORK/AppIcon.icns" "$set"
}

# assemble APP builds the bundle of docs/MACOS-SPEC.md §1 at APP from the universal binaries in
# $WORK/window and $WORK/universal, $WORK/AppIcon.icns, the extension builds in $EXT_OUT and the
# templates.
assemble() {
	local app=$1
	local contents="$app/Contents"
	local progress="$contents/Helpers/Downex Progress.app"
	local agent="$contents/Library/LoginItems/Downex Agent.app"
	local f
	step "bundle $app"
	for f in "$WORK/window/Downex" "$WORK/universal/downexd" "$WORK/universal/downex" \
		"$WORK/universal/downex-nmhost" "$WORK/universal/downexd-agent" "$WORK/AppIcon.icns" \
		"$EXT_OUT/chrome-mv3/manifest.json" "$EXT_OUT/firefox-mv3/manifest.json"; do
		[[ -f $f ]] || die "$f was not built"
	done
	rm -rf "$app"
	# Directories that ditto copies a whole directory into are not created here.
	mkdir -p "$contents/MacOS" "$contents/Helpers" "$contents/Resources/extension" \
		"$progress/Contents/MacOS" "$progress/Contents/Resources" \
		"$agent/Contents/MacOS" "$agent/Contents/Resources"

	render_plist "$PKG/Downex-Info.plist" "$contents/Info.plist"
	ditto "$WORK/window/Downex" "$contents/MacOS/Downex"
	for f in downexd downex downex-nmhost; do
		ditto "$WORK/universal/$f" "$contents/Helpers/$f"
	done
	ditto "$WORK/AppIcon.icns" "$contents/Resources/AppIcon.icns"
	ditto "$EXT_OUT/chrome-mv3" "$contents/Resources/extension/chrome-mv3"
	ditto "$EXT_OUT/firefox-mv3" "$contents/Resources/extension/firefox-mv3"
	render_template "$PKG/EXTENSION.txt" "$contents/Resources/EXTENSION.txt"
	# the licence and the open-source notices travel with the app (GPL-3.0-or-later)
	ditto "$ROOT/LICENSE" "$contents/Resources/LICENSE.txt"
	ditto "$ROOT/ui/THIRD-PARTY-NOTICES.txt" "$contents/Resources/THIRD-PARTY-NOTICES.txt"

	# The progress windows: the same window binary in a bundle of its own whose LSUIElement
	# keeps it out of the Dock (docs/MACOS-SPEC.md §5).
	render_plist "$PKG/DownexProgress-Info.plist" "$progress/Contents/Info.plist"
	ditto "$WORK/window/Downex" "$progress/Contents/MacOS/Downex"
	ditto "$WORK/AppIcon.icns" "$progress/Contents/Resources/AppIcon.icns"

	# The agent: a bundle so that UNUserNotificationCenter works and notifications show the icon.
	render_plist "$PKG/DownexAgent-Info.plist" "$agent/Contents/Info.plist"
	ditto "$WORK/universal/downexd-agent" "$agent/Contents/MacOS/downexd-agent"
	ditto "$WORK/AppIcon.icns" "$agent/Contents/Resources/AppIcon.icns"

	chmod 0755 "$contents/MacOS/Downex" "$contents/Helpers/downexd" "$contents/Helpers/downex" \
		"$contents/Helpers/downex-nmhost" "$progress/Contents/MacOS/Downex" "$agent/Contents/MacOS/downexd-agent"
}

# codesign_one signs one binary or bundle with $IDENTITY: hardened runtime, the entitlements of
# packaging/macos, and a secure timestamp for a Developer ID (an ad-hoc signature has none).
codesign_one() {
	local -a args=(--force --sign "$IDENTITY" --options runtime --entitlements "$PKG/entitlements.plist")
	if [[ $IDENTITY == - ]]; then
		args+=(--timestamp=none)
	else
		args+=(--timestamp)
	fi
	run codesign "${args[@]}" "$@"
}

# sign_app signs the bundle APP inside out: a bundle's signature seals the signatures of the code
# nested in it, so every nested binary and bundle is signed before the bundle around it.
sign_app() {
	local app=$1 tool
	step "signing $app (identity: $IDENTITY)"
	# A signature does not allow extended attributes (Finder info, quarantine) on sealed files.
	run xattr -cr "$app"
	for tool in downexd downex downex-nmhost; do
		codesign_one --identifier "com.downex.$tool" "$app/Contents/Helpers/$tool"
	done
	codesign_one "$app/Contents/Helpers/Downex Progress.app"
	codesign_one "$app/Contents/Library/LoginItems/Downex Agent.app"
	codesign_one "$app"
	run codesign --verify --deep --strict --verbose=2 "$app"
}

# notarize FILE submits a .zip or .dmg to Apple's notary service, waits for the verdict and stops
# the script unless it is Accepted (printing the service's log when there is one).
notarize() {
	local file=$1 result status id
	result="$WORK/notary-$(basename "$file").plist"
	step "notarizing $(basename "$file") (this takes a few minutes)"
	if ! xcrun notarytool submit "$file" --keychain-profile "$NOTARY_PROFILE" --wait --output-format plist >"$result"; then
		printf 'notarytool submit failed:\n' >&2
		cat "$result" >&2 || true
	fi
	status=$(/usr/libexec/PlistBuddy -c 'Print :status' "$result" 2>/dev/null) || status=""
	id=$(/usr/libexec/PlistBuddy -c 'Print :id' "$result" 2>/dev/null) || id=""
	if [[ $status != Accepted ]]; then
		if [[ -n $id ]]; then
			xcrun notarytool log "$id" --keychain-profile "$NOTARY_PROFILE" >&2 || true
		fi
		die "notarization of $file was not accepted (status: ${status:-unknown})"
	fi
}

# make_dmg APP DMG writes the compressed disk image DMG: APP, a link to /Applications to drag it
# onto, and "Read Me.txt".
make_dmg() {
	local app=$1 dmg=$2 stage="$WORK/dmg" try
	step "disk image $dmg"
	rm -rf "$stage"
	mkdir -p "$stage"
	run ditto "$app" "$stage/Downex.app"
	ln -s /Applications "$stage/Applications"
	render_template "$PKG/README-DMG.txt" "$stage/Read Me.txt"
	rm -f "$dmg"
	# hdiutil create sometimes fails with "Resource busy" while Spotlight looks at the new volume.
	for try in 1 2 3; do
		if run hdiutil create -volname "Downex $VERSION" -srcfolder "$stage" -fs HFS+ -format UDZO -ov "$dmg"; then
			break
		fi
		if [[ $try == 3 ]]; then
			die "hdiutil create failed"
		fi
		printf 'hdiutil create failed; trying again in 5 seconds\n' >&2
		sleep 5
	done
	run hdiutil verify "$dmg"
}

print_next_steps() {
	cat <<EOF

Built:
  $APP
  $DMG

Install: open the disk image and drag Downex onto Applications. Then, once per user (and again
after moving the app), register the browser relay from where the app is installed:

  /Applications/Downex.app/Contents/Helpers/downex-nmhost register

and load the browser extension as described in
/Applications/Downex.app/Contents/Resources/EXTENSION.txt.
EOF
	if [[ -n ${NOTARY_PROFILE:-} ]]; then
		return
	fi
	if [[ $IDENTITY == - ]]; then
		printf '\nThis build is ad-hoc signed (no DEVELOPER_ID).'
	else
		printf '\nThis build is signed but not notarized (no NOTARY_PROFILE).'
	fi
	cat <<'EOF'
 It runs on this Mac as built; downloaded on
another Mac, Gatekeeper blocks it until it is approved: right-click Downex in Applications ->
Open (macOS 15 and later: open it once, then System Settings -> Privacy & Security -> Open
Anyway), or remove the quarantine flag, which also lets browsers and the login item start the
helpers:

  xattr -dr com.apple.quarantine /Applications/Downex.app
EOF
}

main() {
	parse_args ${1+"$@"}
	check_host
	cd "$ROOT"
	check_tools
	read_extension_ids
	BUNDLE_VERSION=$(bundle_version "$VERSION")
	EXT_OUT="$ROOT/ext/.output"
	export COREPACK_ENABLE_DOWNLOAD_PROMPT=0
	export MACOSX_DEPLOYMENT_TARGET=$MIN_MACOS
	if [[ $RELEASE == 1 ]]; then
		# No version-control stamps (revision, time, modified files of this working copy) in a
		# published program, the window and the agent included.
		export GOFLAGS="${GOFLAGS:+$GOFLAGS }-buildvcs=false"
	fi
	mkdir -p "$OUT"
	OUT=$(cd -- "$OUT" >/dev/null && pwd -P)
	APP="$OUT/Downex.app"
	DMG="$OUT/Downex-$VERSION.dmg"
	WORK=$(mktemp -d "${TMPDIR:-/tmp}/downex-build.XXXXXX")
	trap 'rm -rf "$WORK"' EXIT

	build_go
	build_extension
	build_window
	make_icns
	assemble "$APP"
	sign_app "$APP"
	if [[ -n ${NOTARY_PROFILE:-} ]]; then
		# The app is notarized and stapled on its own first, so that a copy dragged out of the
		# disk image carries its ticket and opens without a network connection.
		run ditto -c -k --sequesterRsrc --keepParent "$APP" "$WORK/Downex.zip"
		notarize "$WORK/Downex.zip"
		run xcrun stapler staple "$APP"
		run xcrun stapler validate "$APP"
		run spctl --assess --type execute --verbose=4 "$APP"
	fi
	make_dmg "$APP" "$DMG"
	if [[ $IDENTITY != - ]]; then
		run codesign --force --sign "$IDENTITY" --timestamp "$DMG"
	fi
	if [[ -n ${NOTARY_PROFILE:-} ]]; then
		notarize "$DMG"
		run xcrun stapler staple "$DMG"
		run xcrun stapler validate "$DMG"
		run spctl --assess --type open --context context:primary-signature --verbose=4 "$DMG"
	fi
	print_next_steps
}

# Sourced (the tests in packaging/macos do that to call the functions above), it only defines them.
# ${1+"$@"}: no arguments at all under set -u, also in old bash versions.
if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
	main ${1+"$@"}
fi
