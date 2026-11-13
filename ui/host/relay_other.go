//go:build !windows

package host

import "os/exec"

// relayCommand needs nothing outside Windows: no console opens for a child.
func relayCommand(*exec.Cmd) {}
