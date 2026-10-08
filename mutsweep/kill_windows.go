//go:build windows

package main

import (
	"os/exec"
	"strconv"
)

// ownGroup is a no-op on Windows: the tree is killed by pid below rather than
// by group, which needs no attribute here.
func ownGroup(c *exec.Cmd) {}

// killTree kills the command and everything it started.
//
// taskkill /T is the tree; without it the `go test` that mutate started
// survives, keeps the temp directory open, and the cleanup fails with "the
// process cannot access the file because it is being used by another process"
// -- which is how this was found, on this repository's Windows runner.
func killTree(c *exec.Cmd) {
	if c.Process == nil {
		return
	}
	// Without /F first, so the tree is asked rather than shot: mutate restores
	// the file it mutated when it is interrupted.
	_ = exec.Command("taskkill", "/T", "/PID", strconv.Itoa(c.Process.Pid)).Run()
}

// hardKill is what is left when asking did not work.
func hardKill(c *exec.Cmd) {
	if c.Process == nil {
		return
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(c.Process.Pid)).Run()
	_ = c.Process.Kill()
}
