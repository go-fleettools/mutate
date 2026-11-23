//go:build !windows

package main

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// A command that was never started has no process to signal.
func TestKillingACommandThatNeverStartedDoesNothing(t *testing.T) {
	killTree(&exec.Cmd{})
	hardKill(&exec.Cmd{})
}

// waitSignalled starts a process that is NOT its own process group leader, so
// the group kill finds no group and the process itself is what is left to
// signal, and returns how it ended.
func waitSignalled(t *testing.T, kill func(*exec.Cmd)) syscall.Signal {
	t.Helper()
	c := exec.Command("sleep", "60")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	kill(c)
	select {
	case err := <-done:
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("wait = %v, want the process killed by a signal", err)
		}
		return ee.Sys().(syscall.WaitStatus).Signal()
	case <-time.After(20 * time.Second):
		_ = c.Process.Kill()
		t.Fatal("the process was still running 20s after it was asked to stop")
		return 0
	}
}

// kill_unix.go: the fallback to the process itself, asking first and then not.
func TestKillFallsBackToTheProcessWhenThereIsNoGroup(t *testing.T) {
	if got := waitSignalled(t, killTree); got != syscall.SIGTERM {
		t.Errorf("killTree ended the process with %v, want SIGTERM (asking first)", got)
	}
	if got := waitSignalled(t, hardKill); got != syscall.SIGKILL {
		t.Errorf("hardKill ended the process with %v, want SIGKILL", got)
	}
}

// And the normal case, a process leading its own group: the group is what dies.
func TestKillTakesTheWholeGroup(t *testing.T) {
	for name, tt := range map[string]struct {
		kill func(*exec.Cmd)
		want syscall.Signal
	}{"killTree": {killTree, syscall.SIGTERM}, "hardKill": {hardKill, syscall.SIGKILL}} {
		c := exec.Command("sleep", "60")
		ownGroup(c)
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- c.Wait() }()
		tt.kill(c)
		select {
		case err := <-done:
			ee, ok := err.(*exec.ExitError)
			if !ok || ee.Sys().(syscall.WaitStatus).Signal() != tt.want {
				t.Errorf("%s: wait = %v, want %v", name, err, tt.want)
			}
		case <-time.After(20 * time.Second):
			_ = c.Process.Kill()
			t.Errorf("%s: still running after 20s", name)
		}
	}
}
