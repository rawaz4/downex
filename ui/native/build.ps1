<#
.SYNOPSIS
Builds the native window: the opt-in preview downex-ui-native-preview.exe by default, or the
product's downex-ui.exe with -Product (windows/amd64, pure Go).

.DESCRIPTION
1. The resource object rsrc_windows_amd64.syso next to this script (a build output, ignored by
   git): the window's manifest (packaging/windows/downex-ui.exe.manifest), the icon drawn by
   internal/brand (packaging/windows/icon.ico) and the version information, made by the winres
   tool module in winres/ (github.com/tc-hib/winres v0.3.1, the library Wails v2 uses). The
   tool is a module of its own, outside the repository's go.mod, so `go vet ./...` does not
   reach it: this script vets it first.
   -Version is x.y.z for a release or any version text (scripts\build-windows.ps1's default
   "dev"): the resources' numeric versions take its leading numbers (dev -> 0.0.0), their
   strings and main.version the text.
2. The GUI executable: CGO_ENABLED=0, -H windowsgui, without the symbol table and DWARF (-s -w, as
   Wails' production builds), the version in main.version.

The native window needs the x64 build; windows/arm64 refuses at run time. The default output
is dist\native-preview\ in the repository, which git ignores; -Out builds elsewhere.

-Product builds the product's window instead of the preview: today's single-instance locks
(downex/ui/native/glue.lockID set to host.MainInstanceID, so it hands over to and from today's
Wails window, and uses today's ui-theme.json, ui-prefs.json, ui-window.json and ui.log) and
today's version resources (FileDescription and InternalName "Downex", OriginalFilename
"Downex.exe", as Wails writes them), named downex-ui.exe as scripts\build-windows.ps1 names
today's (default output dist\native\downex-ui.exe). Without -Product the build keeps the
preview's own locks and files (glue.PreviewLockID).

.EXAMPLE
powershell -File ui\native\build.ps1
powershell -File ui\native\build.ps1 -Out C:\temp\preview.exe -Version 0.9.2
powershell -File ui\native\build.ps1 -Product -Out C:\temp\native\downex-ui.exe
#>
param(
    [string]$Out = '',
    [string]$Version = '0.9.2',
    [switch]$Product
)
$ErrorActionPreference = 'Stop'
$here = $PSScriptRoot
$repo = (Resolve-Path (Join-Path $here '..\..')).Path
$syso = Join-Path $here 'rsrc_windows_amd64.syso'
if (-not $Out) {
    if ($Product) { $Out = Join-Path $repo 'dist\native\downex-ui.exe' }
    else { $Out = Join-Path $repo 'dist\native-preview\downex-ui-native-preview.exe' }
}
# The version resources' names and the single-instance id (ui/native/glue/names.go).
$resFile = Split-Path $Out -Leaf
$resDesc = 'Downex native window preview'
$ldLock = ''
if ($Product) {
    $resFile = 'Downex.exe'
    $resDesc = 'Downex'
    $ldLock = ' -X downex/ui/native/glue.lockID=downex-ui-6f0d2c1e-4b7a-4e58-9a3d-2c8b7e51f904'
}

& go -C (Join-Path $here 'winres') vet .
if ($LASTEXITCODE -ne 0) { throw "go vet of the winres tool failed ($LASTEXITCODE)" }

& go -C (Join-Path $here 'winres') run . `
    -out $syso `
    -manifest (Join-Path $repo 'packaging\windows\downex-ui.exe.manifest') `
    -icon (Join-Path $repo 'packaging\windows\icon.ico') `
    -version $Version `
    -file $resFile `
    -description $resDesc
if ($LASTEXITCODE -ne 0) { throw "winres failed ($LASTEXITCODE)" }

$dir = Split-Path $Out -Parent
if ($dir -and -not (Test-Path $dir)) { New-Item -ItemType Directory -Force $dir | Out-Null }
$saved = @{ CGO_ENABLED = $env:CGO_ENABLED; GOOS = $env:GOOS; GOARCH = $env:GOARCH }
try {
    $env:CGO_ENABLED = '0'; $env:GOOS = 'windows'; $env:GOARCH = 'amd64'
    & go -C $repo build -trimpath -ldflags "-s -w -H windowsgui -X main.version=$Version$ldLock" -o $Out ./ui/native
    if ($LASTEXITCODE -ne 0) { throw "go build failed ($LASTEXITCODE)" }
} finally {
    foreach ($k in $saved.Keys) { Set-Item "env:$k" $saved[$k] -ErrorAction SilentlyContinue; if ($null -eq $saved[$k]) { Remove-Item "env:$k" -ErrorAction SilentlyContinue } }
}
Get-Item $Out | Select-Object FullName, Length, LastWriteTime
