package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ⛔ A FULL DISK IS NOT A MUTANT THAT DID NOT COMPILE.
//
// 2026-11-27, go-crdt/collab: the disk filled during a full sweep, the build cache
// could no longer be written, and from mutant #72 on 414 of 493 were reported
// "not a mutant" -- the compiler's ENOSPC read as a mutation nothing could test --
// while mutate's own atomic writes failed too, and four source files were left
// holding deliberate defects (guards deleted) because every restore failed and the
// sweep went on to the next mutant. A verdict about the code can only come from a
// machine that could run the code; when it could not, there is no verdict, and the
// sweep must stop and say what failed.
const (
	// envFail: the machine, not the mutation, stopped the run. Never "not a mutant".
	envFail verdict = "ENVIRONMENT"
	// restoreFail: a source file may still hold the deliberate defect. The sweep
	// stops at once: the next mutant would be applied on top of it.
	restoreFail verdict = "RESTORE FAILED"
)

// Exit codes of a sweep that stopped on the machine rather than the code.
const (
	exitEnvironment = 3
	exitRestore     = 4
)

// mutate's own exit codes, which main.go in the repository root defines.
const (
	mutateEnvironment = 3 // the mutation could not be written
	mutateRestore     = 4 // the file could not be put back
)

// environmentSigns are what the operating system says when it, and not the code,
// is at fault. Lower case: matched against a lower-cased line.
var environmentSigns = []string{
	"no space left on device", // ENOSPC
	"disk quota exceeded",     // EDQUOT
	"read-only file system",   // EROFS
	"input/output error",      // EIO
	"too many open files",     // EMFILE / ENFILE
}

// environmentCause returns the line of a command's output that says the machine
// failed, or "".
//
// Only lines the TOOLCHAIN or mutate could have written count. A test that checks
// its own handling of a full disk prints these words too, indented under
// "--- FAIL" or inside a JSON "output" field, and a caught mutant must not turn
// into an abort because of it; so a line that is indented, a test marker, or a
// `go test -json` event other than build output is skipped.
func environmentCause(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if !toolchainCouldHaveWritten(line) {
			continue
		}
		low := strings.ToLower(line)
		for _, s := range environmentSigns {
			if strings.Contains(low, s) {
				return trim(strings.TrimSpace(line), 120)
			}
		}
	}
	return ""
}

func toolchainCouldHaveWritten(line string) bool {
	line = strings.TrimRight(line, "\r")
	if line == "" {
		return false
	}
	if strings.HasPrefix(line, "{") {
		return strings.Contains(line, `"Action":"build-output"`) || strings.Contains(line, `"Action":"build-fail"`)
	}
	if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
		return false
	}
	for _, p := range []string{"---", "===", "ok ", "ok\t", "PASS", "FAIL", "panic:", "goroutine "} {
		if strings.HasPrefix(line, p) {
			return false
		}
	}
	return true
}

// restoreFailure is a file that could not be put back: it still holds what the
// mutation did to it.
type restoreFailure struct {
	path string
	msg  string
}

func (e *restoreFailure) Error() string { return e.msg }

// freeBytes reports the bytes available to an unprivileged writer on the file
// system holding dir. A variable so a test can say the disk is full.
var freeBytes = availableBytes

// checkFreeSpace refuses a machine that is about to run out of disk: a build that
// cannot write its cache reads as a mutant that does not compile.
//
// The threshold is minFreeMB mebibytes (-min-free-mb, default 1024; 0 turns the
// check off) on the file system of the directory the command runs in and of the
// temporary directory the go tool builds in. A file system that cannot be asked
// (Windows) is not refused: that is "unknown", not "full".
func checkFreeSpace(cfg config) error {
	if cfg.minFreeMB <= 0 {
		return nil
	}
	want := uint64(cfg.minFreeMB) << 20
	seen := map[string]bool{}
	for _, dir := range []string{cfg.runDir(), os.TempDir(), os.Getenv("GOCACHE")} {
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		free, err := freeBytes(dir)
		if err != nil {
			continue
		}
		if free < want {
			return fmt.Errorf("only %d MiB free on the file system of %s, under the %d MiB -min-free-mb threshold: "+
				"a build that cannot write its cache reads as a mutant that does not compile, so no verdict would mean anything",
				free>>20, dir, cfg.minFreeMB)
		}
	}
	return nil
}

func (c config) runDir() string {
	if c.runIn != "" {
		return c.runIn
	}
	return c.dir
}

// snapshot is the bytes of every file a sweep will mutate, taken before the first
// mutation, so that after a failed restore the files still wrong can be NAMED.
type snapshot map[string][]byte

func takeSnapshot(cfg config, guards []guard) snapshot {
	s := snapshot{}
	for _, g := range guards {
		p := filepath.Join(cfg.runDir(), g.pathFrom(cfg.runDir()))
		if _, done := s[p]; done {
			continue
		}
		if b, err := os.ReadFile(p); err == nil {
			s[p] = b
		}
	}
	return s
}

// leftMutated lists the files that no longer hold the bytes they had.
func (s snapshot) leftMutated() []string {
	var bad []string
	for p, want := range s {
		if got, err := os.ReadFile(p); err != nil || !bytes.Equal(got, want) {
			bad = append(bad, p)
		}
	}
	sort.Strings(bad)
	return bad
}

// abort says why a sweep stopped, and what it left behind, and returns its exit
// code. The record is never filed: it would certify a sweep that did not run.
func abort(r result, done, total int, snap snapshot, stderr io.Writer) int {
	fmt.Fprintf(stderr, "\nmutsweep: STOPPED at %d/%d, %s:%d -- %s: %s\n", done, total, r.file, r.line, r.verdict, r.note)
	if r.verdict == restoreFail {
		fmt.Fprintln(stderr, "mutsweep: these files may still hold a deliberate defect; restore them (git diff, git checkout -- <file>) BEFORE anything else:")
	} else {
		fmt.Fprintln(stderr, "mutsweep: the machine failed, not the code. Nothing below this line is a verdict, and no record was filed. Free the cause and run the sweep again.")
	}
	left := snap.leftMutated()
	for _, p := range left {
		fmt.Fprintf(stderr, "mutsweep: LEFT MUTATED: %s\n", p)
	}
	if len(left) == 0 {
		fmt.Fprintln(stderr, "mutsweep: every file the sweep touches holds its original bytes")
	}
	if r.verdict == restoreFail {
		return exitRestore
	}
	return exitEnvironment
}
