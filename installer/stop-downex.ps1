# Stops the Downex programs that run from one folder, for Setup and the uninstaller.
#
# Inputs (environment, so that no path has to be quoted on a command line):
#   DOWNEX_SETUP_DIR          the folder whose programs are stopped
#   DOWNEX_SETUP_CLI          the downex.exe used for "daemon status" and "daemon stop"
#   DOWNEX_SETUP_WINDOW_MARK  optional: a file this script creates when it closes a window,
#                             so that a silent update can open the window again afterwards
#
# Only processes whose executable is in DOWNEX_SETUP_DIR are touched; a Downex that runs
# from another folder is left alone. Order: the window first (it would start the daemon
# again), then the daemon through "downex daemon stop" when the daemon on the endpoint is
# one of ours (its running downloads are paused and their progress is saved), then
# whatever is still running from the folder (relay, CLI, a daemon that did not stop).
# Always exits with 0: Setup decides by whether the files can be replaced.

$ErrorActionPreference = 'Continue'
$dir = $env:DOWNEX_SETUP_DIR
$cli = $env:DOWNEX_SETUP_CLI
$mark = $env:DOWNEX_SETUP_WINDOW_MARK
if (-not $dir) { Write-Output 'stop-downex: DOWNEX_SETUP_DIR is not set'; exit 0 }
$dir = [IO.Path]::GetFullPath($dir).TrimEnd('\')

# Processes named $Names whose executable is in $dir.
function Get-Ours([string[]]$Names) {
    @(Get-Process -Name $Names -ErrorAction SilentlyContinue | Where-Object {
        $path = $null
        try { $path = $_.Path } catch { }
        $path -and ([IO.Path]::GetDirectoryName([IO.Path]::GetFullPath($path)) -ieq $dir)
    })
}

# Ends $Process unless it exits within $Milliseconds.
function Stop-Late($Process, [int]$Milliseconds) {
    try {
        if ($Process.WaitForExit($Milliseconds)) { return }
        Write-Output "Ending $($Process.ProcessName) (pid $($Process.Id))"
        $Process.Kill()
        [void]$Process.WaitForExit(5000)
    } catch { }
}

# 1. The window: ask it to close, then end it.
$ui = Get-Ours @('downex-ui')
if ($ui.Count -gt 0 -and $mark) {
    try { [IO.File]::WriteAllText($mark, '') } catch { }
}
foreach ($p in $ui) {
    Write-Output "Closing the Downex window (pid $($p.Id))"
    try { [void]$p.CloseMainWindow() } catch { }
}
foreach ($p in $ui) { Stop-Late $p 5000 }

# 2. The daemon: "downex daemon stop" when the daemon on the endpoint is ours.
$daemons = Get-Ours @('downexd')
if ($daemons.Count -gt 0 -and $cli -and (Test-Path -LiteralPath $cli)) {
    $status = $null
    try { $status = (& $cli daemon status --json 2>$null | Out-String) | ConvertFrom-Json } catch { }
    if ($status -and $status.running -and (@($daemons | ForEach-Object { $_.Id }) -contains [int]$status.pid)) {
        Write-Output "Stopping downexd (pid $($status.pid)); running downloads are paused"
        & $cli daemon stop 2>&1 | ForEach-Object { Write-Output "$_" }
    }
}
foreach ($p in $daemons) { Stop-Late $p 10000 }

# 3. Whatever else still runs from the folder.
foreach ($p in (Get-Ours @('downex-ui', 'downexd', 'downex-nmhost', 'downex'))) { Stop-Late $p 0 }
exit 0
