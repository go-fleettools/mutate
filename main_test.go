package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/go-fleettools/mutate/internal/atomicwrite"
)

// A tool that proves tests can fail has to be able to fail itself.

// ⛔⛔ A RESTORE THAT FAILED IS SAID OUT LOUD, and nothing held that either.
// This is the loudest line in the tool: the tree still holds a deliberate
// defect, the process is about to end, and the next commit would carry it. With
// the guard deleted the write fails in silence and mutate exits looking normal
// — the third of the three failures this tool's doc comment promises to make
// impossible, "the restore was forgotten", restored to possibility.
func TestARestoreThatCannotBeWrittenIsSaidOutLoud(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a read-only directory does not stop a file being created on Windows")
	}
	dir := t.TempDir()
	path := write(t, dir, "g.go", "keep me\n")
	var errb bytes.Buffer
	m, err := newMutation(path, "keep", "drop", "", &errb)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.apply(); err != nil {
		t.Fatal(err)
	}
	// The directory closes AFTER the mutation landed, so the restore is the
	// step with nowhere to write.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	m.restore()
	if !strings.Contains(errb.String(), "COULD NOT RESTORE") {
		t.Errorf("it said %q, and never that the file was NOT put back", errb.String())
	}
	if !strings.Contains(errb.String(), path) {
		t.Errorf("it said %q, which does not name the file left mutated", errb.String())
	}
}

// ⛔⛔ A SECOND RESTORE MUST WAIT FOR THE FIRST, not return because it is
// already under way. On a HUNG mutant the timeout signals the whole group: the
// handler's restore and the main goroutine's restore run together, the loser
// returned at once and os.Exit(130) ended the process between the temporary
// file and the rename -- source left mutated, .g.go.atomicwrite-* left behind.
func TestASecondRestoreWaitsForTheFirstToFinish(t *testing.T) {
	path := write(t, t.TempDir(), "g.go", "keep me\n")
	m, err := newMutation(path, "keep", "drop", "", &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.apply(); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	// Once: a second write (a restore that ignores applied) must not close the
	// channel twice -- that panic, in a goroutine, ends the binary and hides the
	// assertions of every test after it.
	var enter sync.Once
	m.write = func(p string, d []byte, perm fs.FileMode) error {
		enter.Do(func() { close(entered) })
		<-release
		return atomicwrite.Write(p, d, perm)
	}
	first, second := make(chan struct{}), make(chan struct{})
	go func() { m.restore(); close(first) }()
	<-entered
	go func() { m.restore(); close(second) }()
	select {
	case <-second:
		t.Fatal("the second restore returned while the first was still writing")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-first
	<-second
	if b, _ := os.ReadFile(path); string(b) != "keep me\n" {
		t.Errorf("the file holds %q, not its original bytes", b)
	}
}

// ⛔ A MUTATION THAT COULD NOT BE WRITTEN IS NOT A VERDICT ABOUT THE CODE, and
// mutsweep answered SURVIVED for the guard that says so. With it gone, apply
// reports success without having changed a byte: the command then runs against
// UNMUTATED code, passes, and the tool prints "THE SUITE SURVIVED IT. Nothing
// covers this." That is the FIRST FAILURE in this file's own doc comment — a
// mutation that never happened read as a weak test — arrived at from the other
// side.
func TestAMutationThatCannotBeWrittenIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a read-only directory does not stop a file being created on Windows")
	}
	dir := t.TempDir()
	const original = "keep me\n"
	path := write(t, dir, "g.go", original)
	// r-x: the file stays readable and writable, no new entry can be made, so
	// the atomic write has nowhere to put its temporary.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	code, out, errb := runMutate(t, "-file", path, "-from", "keep", "-to", "drop", "--", "true")
	if code != exitEnvironment {
		t.Fatalf("exit %d, want %d (the machine failed)\nstdout: %q\nstderr: %q", code, exitEnvironment, out, errb)
	}
	if !strings.Contains(errb, "mutate:") {
		t.Errorf("stderr does not say who refused: %q", errb)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the file is gone: %v", err)
	}
	if string(after) != original {
		t.Errorf("the file reads %q, want the original back", after)
	}
}

// ⛔ A FULL DISK, injected through the write seam, on either write.
//
// 2026-11-27: the write that applies a mutation failed with ENOSPC and mutate
// exited 2 -- "the mutation is the wrong shape" -- which mutsweep filed as "not a
// mutant" for 414 mutants; the write that RESTORES failed too, and four source
// files were left with their guards deleted. The first is exit 3, the second 4,
// and neither is ever 1 or 2.
func TestAFullDiskIsTheMachinesFailureNotTheMutations(t *testing.T) {
	full := &fs.PathError{Op: "write", Path: "g.go", Err: syscall.ENOSPC}
	for _, tt := range []struct {
		name     string
		failOn   int // which write (1 = the mutation, 2 = the restore) fails
		wantCode int
		wantErr  string
		wantFile string
	}{
		{"mutation", 1, exitEnvironment, "ENVIRONMENT FAILURE", "keep me\n"},
		{"restore", 2, exitRestore, "LEFT MUTATED", "drop me\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := write(t, t.TempDir(), "g.go", "keep me\n")
			calls := 0
			old := writeFile
			t.Cleanup(func() { writeFile = old })
			writeFile = func(p string, d []byte, perm fs.FileMode) error {
				calls++
				if calls == tt.failOn {
					return full
				}
				return atomicwrite.Write(p, d, perm)
			}
			code, out, errb := runMutate(t, "-file", path, "-from", "keep", "-to", "drop", "--", "true")
			if code != tt.wantCode {
				t.Fatalf("exit %d, want %d\nstdout: %q\nstderr: %q", code, tt.wantCode, out, errb)
			}
			if !strings.Contains(errb, tt.wantErr) || !strings.Contains(errb, "no space left on device") {
				t.Errorf("stderr does not name the cause: %q", errb)
			}
			if tt.failOn == 2 && !strings.Contains(errb, path) {
				t.Errorf("stderr does not name the file left mutated: %q", errb)
			}
			if b, _ := os.ReadFile(path); string(b) != tt.wantFile {
				t.Errorf("the file holds %q, want %q", b, tt.wantFile)
			}
		})
	}
}

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// true/false are the smallest commands that pass and fail, so the tool's
// own verdict can be tested without a Go toolchain in the loop.
func runMutate(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

// THE FAILURE THIS TOOL EXISTS FOR, first: a mutation that matches nothing
// runs against unchanged code. The test then passes, and the reading is
// "the test is weak" when the truth is "the mutation never happened". I
// made that mistake by hand; it must be impossible here.
func TestAMutationThatMatchesNothingIsRefused(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "x.go", "package x\n\nconst A = 1\n")
	code, _, errb := runMutate(t, "-file", p, "-from", "const B", "-to", "const C", "--", "false")
	if code != 2 {
		t.Errorf("code=%d, want 2", code)
	}
	if !strings.Contains(errb, "not there") || !strings.Contains(errb, "no-op") {
		t.Errorf("stderr=%q", errb)
	}
	// And the file is untouched.
	if b, _ := os.ReadFile(p); string(b) != "package x\n\nconst A = 1\n" {
		t.Errorf("the file was changed anyway: %q", b)
	}
}

// Replacing every occurrence is a LARGER mutation than the one described,
// and the report would name the small one.
func TestAnAmbiguousMutationIsRefused(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "x.go", "package x\n\nconst A = 1\nconst B = 1\n")
	code, _, errb := runMutate(t, "-file", p, "-from", "= 1", "-to", "= 2", "--", "false")
	if code != 2 || !strings.Contains(errb, "appears 2 times") {
		t.Errorf("code=%d stderr=%q", code, errb)
	}
}

// The ordinary case: the command fails, which means the mutation was
// caught, which means the test covers it.
func TestACaughtMutationSucceeds(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "x.go", "package x\n\nconst A = 1\n")
	code, out, _ := runMutate(t, "-file", p, "-from", "A = 1", "-to", "A = 2",
		"-name", "A becomes 2", "--", "false")
	if code != 0 {
		t.Errorf("code=%d, want 0", code)
	}
	if !strings.Contains(out, "A becomes 2") || !strings.Contains(out, "caught") {
		t.Errorf("stdout=%q", out)
	}
}

// And the one that matters: the suite survived a real change, so nothing
// covers it. Said in those words rather than as a bare non-zero exit.
func TestASurvivingMutationFails(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "x.go", "package x\n\nconst A = 1\n")
	code, _, errb := runMutate(t, "-file", p, "-from", "A = 1", "-to", "A = 2", "--", "true")
	if code != 1 {
		t.Errorf("code=%d, want 1", code)
	}
	if !strings.Contains(errb, "SURVIVED") || !strings.Contains(errb, "Nothing covers this") {
		t.Errorf("stderr=%q", errb)
	}
}

// THE FILE IS RESTORED WHATEVER HAPPENS. A deliberate defect left in a
// tree is one a later commit carries, and that is the worst outcome of
// the three — worse than a wrong verdict, because it leaves.
func TestTheFileIsAlwaysRestored(t *testing.T) {
	const body = "package x\n\nconst A = 1\n"
	for _, cmd := range [][]string{{"true"}, {"false"}, {"/nonexistent/command"}} {
		dir := t.TempDir()
		p := write(t, dir, "x.go", body)
		args := append([]string{"-file", p, "-from", "A = 1", "-to", "A = 2", "--"}, cmd...)
		runMutate(t, args...)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != body {
			t.Errorf("after %v the file reads %q", cmd, b)
		}
	}
}

// A mutant that does not COMPILE is not a failing test, it is no test at
// all — and in a grep for FAIL the two look identical. I read one as the
// other during the session this tool came out of.
//
// Driven through a HELPER PROCESS — this test binary, re-invoked — rather
// than through a shell script. The first version wrote a `.sh` and chmod'd
// it, which Windows cannot execute: the command failed to START, mutate
// read that as the command failing, and reported a mutation as caught. The
// test was wrong about the platform, not the tool, and it took the
// windows-latest lane to say so.
func TestAMutantThatDoesNotCompileIsNotAPass(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "x.go", "package x\n")

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Set HERE, so the subprocess inherits it: mutate runs the command
	// with this process's environment, which is the whole mechanism.
	t.Setenv("MUTATE_HELPER", "1")
	// `false` would normally read as "caught"; the build-failure output has
	// to override that.
	code, _, errb := runMutate(t, "-file", p, "-from", "package x", "-to", "package y",
		"--", self, "-test.run", "TestHelperEmitsABuildFailure")
	if code != 2 {
		t.Errorf("code=%d, want 2 (not 0: a compile error is not a caught mutation)", code)
	}
	if !strings.Contains(errb, "DID NOT COMPILE") || !strings.Contains(errb, "proves nothing") {
		t.Errorf("stderr=%q", errb)
	}
}

// TestHelperEmitsABuildFailure is not a test: it is the command the test
// above runs. It prints what `go build` prints when a package will not
// compile, and fails — which is exactly the pair mutate must not read as a
// caught mutation.
//
// Guarded by an environment variable so it does nothing during an ordinary
// `go test ./...`; the helper-process pattern os/exec's own tests use.
func TestHelperEmitsABuildFailure(t *testing.T) {
	if os.Getenv("MUTATE_HELPER") == "" {
		t.Skip("not a test: the subprocess for TestAMutantThatDoesNotCompileIsNotAPass")
	}
	fmt.Println("# example [build failed]")
	os.Exit(1)
}

// And the classification on its own, with no process at all: these are the
// strings that decide whether a run is reported as a failing test or as no
// test at all.
func TestLooksLikeBuildFailure(t *testing.T) {
	for _, yes := range []string{
		"# example [build failed]",
		"./x.go:4:2: undefined: foo",
		"./x.go:9:1: syntax error: unexpected name",
		"./x.go:3:5: declared and not used: n",
		"build constraints exclude all Go files in .",
	} {
		if !looksLikeBuildFailure(yes) {
			t.Errorf("not recognised as a build failure: %q", yes)
		}
	}
	for _, no := range []string{
		"--- FAIL: TestThing (0.00s)\n    x_test.go:12: want 2, got 1\nFAIL",
		"ok  \texample\t0.1s",
		"",
	} {
		if looksLikeBuildFailure(no) {
			t.Errorf("a test result was read as a build failure: %q", no)
		}
	}
}

// -expect-pass inverts it, for a change that must NOT matter — a comment
// reflowed, a constant renamed. A suite that goes red on one of those is
// pinning its own shape rather than a behaviour.
func TestExpectPass(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "x.go", "package x\n\nconst A = 1\n")
	if code, out, _ := runMutate(t, "-expect-pass", "-file", p,
		"-from", "A = 1", "-to", "A = 1 // a comment", "--", "true"); code != 0 {
		t.Errorf("code=%d out=%q", code, out)
	}
	if code, _, errb := runMutate(t, "-expect-pass", "-file", p,
		"-from", "A = 1", "-to", "A = 1 // a comment", "--", "false"); code != 1 ||
		!strings.Contains(errb, "should not matter") {
		t.Errorf("code=%d stderr=%q", code, errb)
	}
}

func TestUsage(t *testing.T) {
	if code, _, _ := runMutate(t); code != 2 {
		t.Error("no arguments is not a usage error")
	}
	if code, _, _ := runMutate(t, "-file", "x", "-from", "y"); code != 2 {
		t.Error("no command is not a usage error")
	}
}
