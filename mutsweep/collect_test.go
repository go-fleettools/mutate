package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The walk has to see a refusal wherever a statement list can hold one. A
// previous version of this walk looked only at BlockStmt and so never saw the
// two guards inside a case clause of the package it was pointed at -- they were
// correct, which is not the point: the instrument was blind where it claimed to
// look.
func TestCollectSeesEveryStatementList(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x.go", `package x

import "errors"

func InABlock(n int) error {
	if n < 0 {
		return errors.New("negative")
	}
	return nil
}

func InACase(kind int) error {
	switch kind {
	case 1:
		if kind > 9 {
			return errors.New("too big")
		}
		return nil
	}
	return nil
}

func InASelect(c chan int) error {
	select {
	case n := <-c:
		if n < 0 {
			return errors.New("negative")
		}
		return nil
	}
}

// The initialiser form is NOT a refusal this deletes: removing it would remove
// the call as well, which is a larger change than the one that would be
// reported.
func WithAnInitialiser() error {
	if err := do(); err != nil {
		return err
	}
	return nil
}

func do() error { return nil }
`)
	got, err := collect(config{dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	var conds []string
	for _, g := range got {
		conds = append(conds, g.cond)
	}
	want := []string{"n < 0", "kind > 9", "n < 0"}
	if len(got) != len(want) {
		t.Fatalf("found %d guards %v, want %d %v", len(got), conds, len(want), want)
	}
	for i := range want {
		if conds[i] != want[i] {
			t.Errorf("guard %d is %q, want %q", i, conds[i], want[i])
		}
	}
	for _, g := range got {
		if !strings.HasSuffix(strings.TrimSpace(g.from), "}") {
			t.Errorf("the text to delete does not end at the guard: %q", g.from)
		}
		if !strings.HasPrefix(g.from, g.to) {
			t.Errorf("what replaces it is not its own context: from=%q to=%q", g.from, g.to)
		}
	}
}

// A sweep with nothing to delete is not a clean sweep: a changed predicate, a
// wrong directory and a build tag that excludes everything all land here.
func TestNothingToDeleteIsAnError(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x.go", "package x\n\nfunc F() int { return 1 }\n")
	var out, errOut strings.Builder
	if code := run([]string{"-dir", dir, "--", "true"}, &out, &errOut); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "the walk is wrong") {
		t.Errorf("the message does not say the walk is wrong: %q", errOut.String())
	}
}

func TestUsageWithoutACommand(t *testing.T) {
	var out, errOut strings.Builder
	if code := run([]string{"-dir", "."}, &out, &errOut); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "usage:") {
		t.Errorf("no usage line: %q", errOut.String())
	}
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Two guards in one file often have identical text -- `if used <= 0 { return
// ..., false }` twice in a decoder is ordinary -- and mutate rightly refuses an
// ambiguous replacement. So each one is widened with preceding source until it
// occurs exactly once.
//
// Before this, both were reported as "not a mutant", which is wrong twice over:
// they are mutable, and on the package that surfaced it both were REAL
// survivors that a sweep would have dropped in silence.
func TestIdenticalGuardsAreStillAddressable(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x.go", `package x

func Decode(a, b []byte) bool {
	n, used := uvarint(a)
	if used <= 0 {
		return false
	}
	_ = n
	m, used2 := uvarint(b)
	if used2 <= 0 {
		return false
	}
	_ = m
	return true
}

func uvarint(b []byte) (int, int) { return 0, len(b) }
`)
	got, err := collect(config{dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("found %d guards, want 2", len(got))
	}
	src, err := os.ReadFile(filepath.Join(dir, "x.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range got {
		if n := strings.Count(string(src), g.from); n != 1 {
			t.Errorf("line %d: the text to replace appears %d times, so mutate would refuse it:\n%q",
				g.line, n, g.from)
		}
		// And replacing it must delete the guard and nothing else.
		after := strings.Replace(string(src), g.from, g.to, 1)
		if len(after) >= len(string(src)) {
			t.Errorf("line %d: the replacement did not remove anything", g.line)
		}
		// Counted on the WHOLE condition: "if used" is a substring of
		// "if used2", and counting the shorter one counts both.
		left := strings.Count(after, "if used <= 0") + strings.Count(after, "if used2 <= 0")
		if left != 1 {
			t.Errorf("line %d: %d guards left after the replacement, want 1", g.line, left)
		}
	}
}

// A sweep runs a command of somebody's choosing. If that command reaches this
// tool again, the second sweep runs the command again, and each mutant starts a
// sweep of its own. That is not hypothetical: a config built without a run
// directory once ran the command in this tool's own directory, where
// `go test ./...` is this tool's own suite, and the machine reached 10,837
// processes and a load average of 532 before anybody noticed.
//
// The environment carries the refusal, because the recursion is about the
// COMMAND and not about a directory: a path check would miss every other way
// the command can reach back here.
func TestANestedSweepRefusesToStart(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "mutsweep"+exeSuffix())
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the tool: %v\n%s", err, out)
	}

	c := exec.Command(bin, "-dir", ".", "--", "true")
	c.Env = append(os.Environ(), "MUTSWEEP_ACTIVE=1")
	out, err := c.CombinedOutput()
	if err == nil {
		t.Fatal("a nested sweep started: nothing stops a command from starting one per mutant")
	}
	if !strings.Contains(string(out), "refusing to run inside another sweep") {
		t.Errorf("it refused for some other reason: %s", out)
	}

	// The control: without the marker, the same invocation gets as far as
	// looking for guards, so the refusal above is about the marker and not
	// about the arguments.
	c2 := exec.Command(bin, "-dir", ".", "--", "true")
	c2.Env = append(os.Environ(), "MUTSWEEP_ACTIVE=")
	out2, _ := c2.CombinedOutput()
	if strings.Contains(string(out2), "refusing to run inside another sweep") {
		t.Errorf("it refuses even outside a sweep: %s", out2)
	}
}

// -only narrows a collection to the refusals named, and nothing else.
//
// This is the loop a report puts somebody in: a sweep names survivors, tests
// get written, and the question is whether those tests kill them -- which a
// full sweep answers in tens of minutes and this answers in seconds.
func TestOnlyKeepsExactlyWhatItNames(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.go", `package x

import "errors"

func One(n int) error {
	if n < 0 {
		return errors.New("one")
	}
	if n > 9 {
		return errors.New("two")
	}
	return nil
}
`)
	write(t, dir, "b.go", `package x

import "errors"

func Two(n int) error {
	if n == 7 {
		return errors.New("three")
	}
	return nil
}
`)

	all, err := collect(config{dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("the control found %d refusals, want 3 -- the fixture is wrong, not -only", len(all))
	}

	// Name the second guard of a.go and the one in b.go, leaving the first out.
	want := []target{{file: "a.go", line: all[1].line}, {file: "b.go", line: all[2].line}}
	got, err := collect(config{dir: dir, only: want})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("-only kept %d refusals, want 2", len(got))
	}
	for i, g := range got {
		if g.file != want[i].file || g.line != want[i].line {
			t.Errorf("kept %s:%d, want %s", g.file, g.line, want[i])
		}
	}
	if strings.Contains(got[0].cond, "n < 0") {
		t.Error("-only kept the guard it was not given")
	}
}

// A target that names no refusal is an error naming the target.
//
// Both ways an -only list goes stale -- the file edited since the report, the
// line mistyped -- end as a collection of nothing. mutsweep already refuses an
// empty collection, but its message says the WALK is wrong, which is the wrong
// place to send somebody when the walk is right and the name is not.
func TestOnlyRefusesATargetThatIsNotThere(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.go", `package x

import "errors"

func One(n int) error {
	if n < 0 {
		return errors.New("one")
	}
	return nil
}
`)
	real, err := collect(config{dir: dir})
	if err != nil || len(real) != 1 {
		t.Fatalf("the control found %d refusals (%v), want 1", len(real), err)
	}

	// The control first: the line that IS there is kept, so what follows is
	// about the missing line and not about -only refusing everything.
	if got, err := collect(config{dir: dir, only: []target{{file: "a.go", line: real[0].line}}}); err != nil || len(got) != 1 {
		t.Fatalf("the line that is there gave %d refusals (%v), want 1", len(got), err)
	}

	_, err = collect(config{dir: dir, only: []target{{file: "a.go", line: real[0].line + 100}}})
	if err == nil {
		t.Fatal("a line that holds no refusal was accepted, so a stale -only list would sweep nothing and look clean")
	}
	for _, want := range []string{"a.go", "not there"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal says %q, which does not name %q", err, want)
		}
	}
}

// -only says which files to read, so a package of a thousand files is not
// parsed to answer a question about two of them.
func TestOnlyNeedsNoFilesList(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.go", `package x

import "errors"

func One(n int) error {
	if n < 0 {
		return errors.New("one")
	}
	return nil
}
`)
	// Unparseable, so reading it at all is a failure this test can see.
	write(t, dir, "b.go", "package x\n\nthis is not Go\n")

	got, err := collect(config{dir: dir, only: []target{{file: "a.go", line: 6}}})
	if err != nil {
		t.Fatalf("-only read a file it was not asked about: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("kept %d refusals, want 1", len(got))
	}
}

// The spellings -only refuses, each for its own reason.
func TestOnlyReadsItsTargets(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []target
	}{
		{"a.go:12", []target{{"a.go", 12}}},
		{"a.go:12,b/c.go:3", []target{{"a.go", 12}, {"b/c.go", 3}}},
		{" a.go:12 , b.go:3 ", []target{{"a.go", 12}, {"b.go", 3}}},
	} {
		got, err := parseTargets(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("%q gave %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%q gave %v, want %v", c.in, got, c.want)
				break
			}
		}
	}
	for _, bad := range []string{"a.go", "a.go:", ":12", "a.go:0", "a.go:-3", "a.go:x"} {
		if _, err := parseTargets(bad); err == nil {
			t.Errorf("%q was accepted, and would have matched nothing", bad)
		}
	}
}
