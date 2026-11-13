package platform

import "golang.org/x/sys/windows"

// RaiseDownloadPriority puts the calling process (downexd) in the above-normal priority
// class: when the processor is busy, as on a low-end laptop running other programs or
// Downex's own windows, receiving a download is scheduled before them, so the download
// keeps its speed. downexd itself uses a few percent of one core. The programs it starts
// (yt-dlp, Deno, the PAC helper) get the normal class, as Windows only passes the idle
// and below-normal classes on.
func RaiseDownloadPriority() error {
	return windows.SetPriorityClass(windows.CurrentProcess(), windows.ABOVE_NORMAL_PRIORITY_CLASS)
}
