package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A tool that proves tests can fail has to be able to fail itself.

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
// other during this session's bk work.
func TestAMutantThatDoesNotCompileIsNotAPass(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "x.go", "package x\n")
	// `false` would normally read as "caught"; the build-failure output
	// must override that.
	sh := write(t, dir, "fake.sh", "#!/bin/sh\necho '# example [build failed]'\nexit 1\n")
	if err := os.Chmod(sh, 0o755); err != nil {
		t.Fatal(err)
	}
	code, _, errb := runMutate(t, "-file", p, "-from", "package x", "-to", "package y", "--", sh)
	if code != 2 {
		t.Errorf("code=%d, want 2 (not 0: a compile error is not a caught mutation)", code)
	}
	if !strings.Contains(errb, "DID NOT COMPILE") || !strings.Contains(errb, "proves nothing") {
		t.Errorf("stderr=%q", errb)
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
