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

// The initialiser form IS a refusal: skipping it made a guard invisible, so a
// sweep read as clean. It is mutated by making its condition unreachable, never
// deleted, because deleting it would remove the call as well.
func WithAnInitialiser() error {
	if err := do(); err != nil {
		return err
	}
	return nil
}

func do() error { return nil }
`)
	got, _, err := collect(config{dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	var conds []string
	for _, g := range got {
		conds = append(conds, g.cond)
	}
	want := []string{"n < 0", "kind > 9", "n < 0", "err != nil"}
	if len(got) != len(want) {
		t.Fatalf("found %d guards %v, want %d %v", len(got), conds, len(want), want)
	}
	for i := range want {
		if conds[i] != want[i] {
			t.Errorf("guard %d is %q, want %q", i, conds[i], want[i])
		}
	}
	for _, g := range got {
		if g.cond == "err != nil" {
			// The init-clause guard: its mutation keeps the init statement and
			// the body, and only makes the condition unreachable.
			if !strings.HasSuffix(g.from, g.cond) || !strings.HasSuffix(g.to, "false && ("+g.cond+")") {
				t.Errorf("init-clause mutation is not false && (C): from=%q to=%q", g.from, g.to)
			}
			continue
		}
		if !strings.HasSuffix(strings.TrimSpace(g.from), "}") {
			t.Errorf("the text to delete does not end at the guard: %q", g.from)
		}
		if !strings.HasPrefix(g.from, g.to) {
			t.Errorf("what replaces it is not its own context: from=%q to=%q", g.from, g.to)
		}
		// Deleting is the first try; false && (C) is held back for when it does
		// not compile.
		if !strings.HasSuffix(g.altTo, "false && ("+g.cond+")") || strings.HasSuffix(g.altFrom, "}") {
			t.Errorf("no fallback mutation for %q: altFrom=%q altTo=%q", g.cond, g.altFrom, g.altTo)
		}
	}
}

// ⛔ The other half of "what is a refusal" — the shapes that are NOT one. Both
// of the sentences that reject a shape SURVIVED their own mutation at the moment
// the collection was restructured: nothing in the suite had an `if` whose body is
// long, and nothing had one that does not end in a return, so "a refusal is a
// SHORT body ending in a RETURN" was enumerated nowhere and asserted nowhere.
//
// ⛔ And one of the two could not be held by any assertion as it was written:
// `len(List) == 0 || len(List) > 3` guarded an index of `len-1` on the next line,
// so deleting it made the suite red with a PANIC. A panic red is luck — it is the
// line BELOW doing the catching, and the next change that reorders them makes the
// same mutant survive in silence. The emptiness is now refused by the return
// test, which a nil last statement fails like any other non-return, and what is
// left is two bounds a test can state: the length, and the return.
func TestWhatIsNotARefusal(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x.go", `package x

import "errors"

// Four statements ending in a return: too long to be a refusal. A guard is
// read by a human in one glance; a body this long is a branch of the function.
func Longer(n int) error {
	if n > 100 {
		a := n
		b := a
		c := b
		return errors.New("big")
	}
	return nil
}

// Exactly three: the bound is inclusive, which is what makes the line above a
// bound rather than an accident.
func AtTheBound(n int) error {
	if n > 30 {
		a := n
		_ = a
		return errors.New("medium")
	}
	return nil
}

// Short, but its last statement is not a return: it decides, it does not refuse.
func NotAReturn(n int) int {
	out := 0
	if n > 10 {
		out = -n
	}
	return out
}

// An empty body. Nothing to delete, nothing to refuse -- and this is the shape
// that used to be rejected by a length check whose removal panicked.
func AnEmptyBody(n int) int {
	if n > 5 {
	}
	return n
}
`)
	got, _, err := collect(config{dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	var conds []string
	for _, g := range got {
		conds = append(conds, g.cond)
	}
	want := []string{"n > 30"}
	if len(conds) != len(want) || conds[0] != want[0] {
		t.Fatalf("enumerated %v, want exactly %v -- a shape that is not a refusal was swept", conds, want)
	}
}

// ⛔⛔ An `else` branch used to disqualify a refusal, and the exclusion was
// invisible: not swept, not counted, not reported, so a package of `if`/`else`
// guards read as a CLEAN sweep. This is the control file the gap was measured
// with at v0.13.0 — five shapes, and only the first two and the last were
// enumerated.
//
// ⭐ The pair that isolates the cause is D and E: the same condition, the same
// two branches, differing ONLY by the `else`. Without it, "an if with an else is
// skipped" and "an else-if condition is skipped" are two readings of the same
// symptom, and the first version of this finding named the wrong one (it blamed
// the init clause, which #29 had already fixed).
func TestCollectSeesAnIfThatHasAnElseBranch(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x.go", `package x

func A(x int) int {
	if x > 0 {
		return 1
	}
	return 0
}

func B(m map[string]int, k string) int {
	if v, ok := m[k]; ok {
		return v
	}
	return -1
}

func C(x int) int {
	if x > 10 {
		return 2
	} else if x > 5 {
		return 1
	}
	return 0
}

func D(x int) int {
	if x > 3 {
		return 1
	} else {
		return 0
	}
}

func E(x int) int {
	if x > 3 {
		return 1
	}
	return 0
}
`)
	got, _, err := collect(config{dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	var conds []string
	for _, g := range got {
		conds = append(conds, g.cond)
	}
	want := []string{"x > 0", "ok", "x > 10", "x > 5", "x > 3", "x > 3"}
	if len(conds) != len(want) {
		t.Fatalf("found %d guards %v, want %d %v", len(conds), conds, len(want), want)
	}
	for i := range want {
		if conds[i] != want[i] {
			t.Errorf("guard %d is %q, want %q", i, conds[i], want[i])
		}
	}
	// ⛔ Deletion is the WRONG mutation for an if that has an else: it removes
	// both arms, which is "the decision removed" and not "the guard removed", and
	// the suite would then go red for a reason that says nothing about the
	// condition. The else-bearing ones must carry false && (C) as their PRIMARY
	// mutation, exactly as an init-clause guard does.
	for _, i := range []int{2, 3, 4} {
		g := got[i]
		if !strings.HasSuffix(g.to, "false && ("+g.cond+")") {
			t.Errorf("guard %q (with an else) is not mutated by false && (C): to=%q", g.cond, g.to)
		}
		if strings.HasSuffix(strings.TrimSpace(g.from), "}") {
			t.Errorf("guard %q (with an else) would be DELETED, both arms with it: from=%q", g.cond, g.from)
		}
		if g.altFrom != "" || g.altTo != "" {
			t.Errorf("guard %q offers deletion as a fallback: altFrom=%q altTo=%q", g.cond, g.altFrom, g.altTo)
		}
	}
	// And the control's other half: the same guard without an else is still
	// deleted, and still keeps false && (C) as the fallback for when deleting
	// does not compile.
	last := got[len(got)-1]
	if !strings.HasSuffix(strings.TrimSpace(last.from), "}") {
		t.Errorf("the else-less guard is no longer deleted: from=%q", last.from)
	}
	if !strings.HasSuffix(last.altTo, "false && ("+last.cond+")") {
		t.Errorf("the else-less guard lost its fallback: altTo=%q", last.altTo)
	}
}

// ⛔ A chain of three, because "it handles one else if" is how the next shape
// gets forgotten: the walk follows the Else field for as long as it is an if.
func TestCollectFollowsAWholeElseIfChain(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x.go", `package x

func F(x int) string {
	if x > 30 {
		return "a"
	} else if x > 20 {
		return "b"
	} else if x > 10 {
		return "c"
	} else {
		return "d"
	}
}
`)
	got, _, err := collect(config{dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	var conds []string
	for _, g := range got {
		conds = append(conds, g.cond)
	}
	want := []string{"x > 30", "x > 20", "x > 10"}
	if len(conds) != len(want) {
		t.Fatalf("found %d guards %v, want %d %v", len(conds), conds, len(want), want)
	}
	for i := range want {
		if conds[i] != want[i] {
			t.Errorf("guard %d is %q, want %q", i, conds[i], want[i])
		}
	}
	// Each condition is reported once. A chain walked both as a chain and as a
	// statement would double every refusal after the first, and the sweep's own
	// count is what a campaign is audited against.
	seen := map[string]int{}
	for _, c := range conds {
		seen[c]++
	}
	for c, n := range seen {
		if n != 1 {
			t.Errorf("condition %q reported %d times", c, n)
		}
	}
}

// -boundary mode reaches an else-if's comparison too, which is the off-by-one a
// chain of range tests hides.
func TestBoundaryVisitsAnElseIfCondition(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module x\n\ngo 1.27.1\n")
	write(t, dir, "x.go", `package x

func F(n int) int {
	if n > 10 {
		return 2
	} else if n > 5 {
		return 1
	}
	return 0
}
`)
	got, _, err := collect(config{dir: dir, mode: modeBoundary})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("found %d comparisons, want 2 (both arms of the chain): %+v", len(got), got)
	}
	for _, g := range got {
		if !strings.HasSuffix(g.to, ">=") {
			t.Errorf("comparison %q was not moved to the other side of its edge: to=%q", g.cond, g.to)
		}
	}
}

// -boundary mode visits the condition of an init-clause if as well.
func TestBoundaryVisitsInitClauseConditions(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module x\n\ngo 1.27.1\n")
	write(t, dir, "x.go", `package x

import "errors"

func F(n int) error {
	if m := n * 2; m > 10 {
		return errors.New("big")
	}
	return nil
}
`)
	got, _, err := collect(config{dir: dir, mode: modeBoundary})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0].cond, "m > 10") || !strings.HasSuffix(got[0].to, ">=") {
		t.Fatalf("want the m > 10 comparison flipped to >=, got %+v", got)
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
	got, _, err := collect(config{dir: dir})
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

	// A fixture directory, and not `.`, for two reasons that both bit.
	//
	// The control below runs a real sweep. Pointed at `.` it swept THIS
	// package's own source in the working tree, mutating and restoring the
	// checked-out files of the repository the test runs in -- which is the
	// neighbourhood of the 10,837 processes above, and no place for a control.
	//
	// And it drove `mutate` from PATH, since that is the default. The tool says
	// so loudly when that fails -- "NOT RUN ... the mutate command could not be
	// started" -- but this control asserted only that the output does NOT
	// contain the nested-sweep refusal, which is true of that output too. So on
	// a machine with no `mutate` installed it passed with nothing having run.
	// It gets the one built from this repository, and an assertion that reads
	// the summary.
	dir := t.TempDir()
	write(t, dir, "x.go", `package x

import "errors"

func One(n int) error {
	if n < 0 {
		return errors.New("negative")
	}
	return nil
}
`)
	mutateBin := buildMutate(t)

	c := exec.Command(bin, "-dir", dir, "-mutate-bin", mutateBin, "--", "true")
	c.Env = append(os.Environ(), "MUTSWEEP_ACTIVE=1")
	out, err := c.CombinedOutput()
	if err == nil {
		t.Fatal("a nested sweep started: nothing stops a command from starting one per mutant")
	}
	if !strings.Contains(string(out), "refusing to run inside another sweep") {
		t.Errorf("it refused for some other reason: %s", out)
	}

	// The control: without the marker, the same invocation sweeps, so the
	// refusal above is about the marker and not about the arguments.
	c2 := exec.Command(bin, "-dir", dir, "-mutate-bin", mutateBin, "--", "true")
	c2.Env = append(os.Environ(), "MUTSWEEP_ACTIVE=")
	out2, _ := c2.CombinedOutput()
	if strings.Contains(string(out2), "refusing to run inside another sweep") {
		t.Errorf("it refuses even outside a sweep: %s", out2)
	}
	// "1 survived" and not "1 refusals": the count is printed before anything
	// runs, and the summary only after. Measured both ways -- with a working
	// mutate the summary says "1 survived", and with a missing one it says
	// "0 survived" under a NOT RUN line.
	if !strings.Contains(string(out2), "1 survived") {
		t.Errorf("the control reached no verdict, so it proves nothing: %s", out2)
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

	all, _, err := collect(config{dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("the control found %d refusals, want 3 -- the fixture is wrong, not -only", len(all))
	}

	// Name the second guard of a.go and the one in b.go, leaving the first out.
	want := []target{{file: "a.go", line: all[1].line}, {file: "b.go", line: all[2].line}}
	got, _, err := collect(config{dir: dir, only: want})
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
	real, _, err := collect(config{dir: dir})
	if err != nil || len(real) != 1 {
		t.Fatalf("the control found %d refusals (%v), want 1", len(real), err)
	}

	// The control first: the line that IS there is kept, so what follows is
	// about the missing line and not about -only refusing everything.
	if got, _, err := collect(config{dir: dir, only: []target{{file: "a.go", line: real[0].line}}}); err != nil || len(got) != 1 {
		t.Fatalf("the line that is there gave %d refusals (%v), want 1", len(got), err)
	}

	_, _, err = collect(config{dir: dir, only: []target{{file: "a.go", line: real[0].line + 100}}})
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

	got, _, err := collect(config{dir: dir, only: []target{{file: "a.go", line: 6}}})
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

// A file the build excludes is refused when it is NAMED.
//
// This is the one case where a sweep can report a clean package it never
// compiled. The command does not build the file, so deleting a refusal in it
// changes nothing the command can see, the suite passes, and the verdict is
// SURVIVED -- "nothing covers this" -- when the truth is that nothing LOOKED.
//
// Measured on go-crdt/collab before this check existed: its root holds 286
// refusals of which 41 are under `js && wasm`, and one of those deleted gave
// "THE SUITE SURVIVED IT" after ninety-three seconds of running a suite that
// never compiled the file.
func TestANamedFileTheBuildExcludesIsRefused(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "native.go", `package x

import "errors"

func One(n int) error {
	if n < 0 {
		return errors.New("one")
	}
	return nil
}
`)
	// A constraint no host this runs on satisfies, and not a GOOS name, so the
	// fixture does not depend on which platform the test runs on.
	write(t, dir, "nowhere.go", `//go:build mutsweep_never

package x

import "errors"

func Two(n int) error {
	if n == 7 {
		return errors.New("two")
	}
	return nil
}
`)

	// The control, and it comes first: the file that IS in the build is found,
	// so what follows is about the exclusion and not about collect refusing
	// every named file.
	got, _, err := collect(config{dir: dir, files: []string{"native.go"}})
	if err != nil || len(got) != 1 {
		t.Fatalf("the file in the build gave %d refusals (%v), want 1", len(got), err)
	}

	_, _, err = collect(config{dir: dir, files: []string{"nowhere.go"}})
	if err == nil {
		t.Fatal("a file the build excludes was accepted, and every refusal in it would be reported as SURVIVED")
	}
	for _, want := range []string{"nowhere.go", "SURVIVED", "GOOS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal says %q, which does not mention %q", err, want)
		}
	}

	// Naming it through -only is the same claim, and gets the same answer.
	if _, _, err := collect(config{dir: dir, only: []target{{file: "nowhere.go", line: 8}}}); err == nil {
		t.Error("-only accepted a file the build excludes")
	}
}

// A directory sweep is not a claim about every file in it, so an excluded file
// is skipped -- and RETURNED, because skipping it quietly is how a campaign
// comes to believe it covered a package it never compiled.
func TestADirectorySweepSkipsAndReportsWhatTheBuildExcludes(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "native.go", `package x

import "errors"

func One(n int) error {
	if n < 0 {
		return errors.New("one")
	}
	return nil
}
`)
	write(t, dir, "nowhere.go", `//go:build mutsweep_never

package x

import "errors"

func Two(n int) error {
	if n == 7 {
		return errors.New("two")
	}
	return nil
}
`)

	got, excluded, err := collect(config{dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("swept %d refusals, want 1 -- the excluded file's refusal must not be in the list", len(got))
	}
	if len(excluded) != 1 || excluded[0] != "nowhere.go" {
		t.Fatalf("reported %v as excluded, want [nowhere.go]", excluded)
	}
	// And it is the right one that survived the filter.
	if !strings.Contains(got[0].cond, "n < 0") {
		t.Errorf("the refusal kept is %q, which is not the one in the file the build includes", got[0].cond)
	}
}

// ⛔ -tally answers WHICH file, because the total cannot be planned with.
//
// go-crdt/collab's root package was swept in three chunks of 55, 61 and 32
// refusals, and "148 refusals, that closes the root package" was published --
// while the package holds 245. Ninety-seven refusals in eight files had been
// in no chunk at all. `mutsweep -dir . -- true` prints the total before
// running anything, and that total is what caught it afterwards; it says how
// many and never which, so the chunk list could only be checked against a grep
// of the sweep's own output. That is why the gap survived a check already
// written down as the remedy.
//
// The first property asserted here is that it runs NOTHING: a tally that swept
// would cost what it exists to plan.
func TestTallyCountsPerFileAndRunsNothing(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "many.go", `package x

import "errors"

func A(n int) error {
	if n < 0 {
		return errors.New("one")
	}
	if n > 9 {
		return errors.New("two")
	}
	if n == 5 {
		return errors.New("three")
	}
	return nil
}
`)
	write(t, dir, "one.go", `package x

import "errors"

func B(s string) error {
	if s == "" {
		return errors.New("empty")
	}
	return nil
}
`)
	// A third file with the same count as one.go, to pin the tie-break.
	write(t, dir, "also_one.go", `package x

import "errors"

func C(s string) error {
	if s == "?" {
		return errors.New("huh")
	}
	return nil
}
`)

	marker := filepath.Join(dir, "it-ran")
	var out, errOut strings.Builder
	code := run([]string{"-dir", dir, "-tally", "--", "touch", marker}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the command ran: a tally must cost nothing, which is the whole of why it exists")
	}

	got := out.String()
	if !strings.Contains(got, "5 refusals in 3 file(s)") {
		t.Errorf("the header does not count them: %q", got)
	}
	for _, want := range []string{"3  many.go", "1  also_one.go", "1  one.go"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q is not in the tally:\n%s", want, got)
		}
	}
	// Most first, and the name as the tie-break so two runs agree.
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 4 {
		t.Fatalf("want a header and three files:\n%s", got)
	}
	if !strings.Contains(lines[1], "many.go") {
		t.Errorf("the file with the most refusals is not first: %q", lines[1])
	}
	if !strings.Contains(lines[2], "also_one.go") || !strings.Contains(lines[3], "one.go") {
		t.Errorf("the tie was not broken by name: %q then %q", lines[2], lines[3])
	}
}

// It needs no command, since it runs none -- and WITHOUT it a missing command
// is still a usage error, because a sweep with nothing to run would report
// every guard as surviving a suite that was never there.
func TestTallyNeedsNoCommandAndASweepStillDoes(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x.go", `package x

import "errors"

func A(n int) error {
	if n < 0 {
		return errors.New("negative")
	}
	return nil
}
`)
	var out, errOut strings.Builder
	if code := run([]string{"-dir", dir, "-tally"}, &out, &errOut); code != 0 {
		t.Errorf("-tally with no command exited %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "1 refusals in 1 file(s)") {
		t.Errorf("it counted nothing: %q", out.String())
	}

	out.Reset()
	errOut.Reset()
	if code := run([]string{"-dir", dir}, &out, &errOut); code != 2 {
		t.Errorf("a sweep with no command exited %d, want 2 (stderr: %q)", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "-tally") {
		t.Errorf("the usage does not mention the mode that needs no command: %q", errOut.String())
	}
}

// -files narrows the tally, which is how a chunk is checked BEFORE it is run:
// the count the chunk will sweep, against the count the package holds.
func TestTallyRespectsTheFileList(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.go", `package x

import "errors"

func A(n int) error {
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

func B(s string) error {
	if s == "" {
		return errors.New("empty")
	}
	return nil
}
`)
	var out, errOut strings.Builder
	if code := run([]string{"-dir", dir, "-files", "a.go", "-tally"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "2 refusals in 1 file(s)") {
		t.Errorf("the chunk's own count is wrong: %q", got)
	}
	if strings.Contains(got, "b.go") {
		t.Errorf("a file the list did not name was counted: %q", got)
	}
	// And the control: the whole package is the larger number, which is the
	// comparison the flag exists to make.
	out.Reset()
	if code := run([]string{"-dir", dir, "-tally"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "3 refusals in 2 file(s)") {
		t.Errorf("the package's count is wrong: %q", out.String())
	}
}
