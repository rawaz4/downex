//go:build unix

package tools

import (
	"os/exec"
	"syscall"
)

// killAtExit is false: once the child is waited for, its group id is safe to signal
// only while members remain (release).
const killAtExit = false

// groupTree is a child leading its own process group.
type groupTree struct{ pid int }

// startTree starts cmd as the leader of a new process group; memory is not limited.
func startTree(cmd *exec.Cmd, _ int64) (procTree, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &groupTree{pid: cmd.Process.Pid}, nil
}

// kill kills the group while its leader is not yet waited for, so its id is still ours.
func (g *groupTree) kill() { _ = syscall.Kill(-g.pid, syscall.SIGKILL) }

// release kills what remains of the group when members still hold the output pipes: a
// group with members keeps its id, so it cannot have been reused.
func (g *groupTree) release(stragglers bool) {
	if stragglers {
		_ = syscall.Kill(-g.pid, syscall.SIGKILL)
	}
}
