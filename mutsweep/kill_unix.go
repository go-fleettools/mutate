//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// ownGroup puts the command in a process group of its own, so that killing it
// kills what it started.
//
// A sweep that gives up on a mutant must not leave the work behind: mutate runs
// `go test`, and killing mutate alone orphans the test binary. On Windows that
// shows at once -- the orphan holds the temp directory and the cleanup fails --
// and on Unix it is quieter and just as real: a hundred guards could leave a
// hundred test processes running.
func ownGroup(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree ends the command and everything it started, asking first.
//
// The asking matters: mutate restores the file it mutated when it is
// interrupted, and SIGKILL is not catchable. Killing outright left the
// deliberate defect in the tree -- measured, the exact failure mutate exists to
// prevent, reintroduced by the thing driving it.
func killTree(c *exec.Cmd) {
	if c.Process == nil {
		return
	}
	// The negative pid is the group; the process itself is the fallback for the
	// window before the group exists.
	if err := syscall.Kill(-c.Process.Pid, syscall.SIGTERM); err != nil {
		_ = c.Process.Signal(syscall.SIGTERM)
	}
}

// hardKill is what is left when asking did not work.
func hardKill(c *exec.Cmd) {
	if c.Process == nil {
		return
	}
	if err := syscall.Kill(-c.Process.Pid, syscall.SIGKILL); err != nil {
		_ = c.Process.Kill()
	}
}
