package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// ⛔ This file sorts FIRST on purpose. A test that panics ends the whole test
// binary, so the tests after it never run, and a guard held only by that panic
// is "caught" by luck (mutsweep: `caught by PANIC`). The tests here run before
// any that could panic, recover the panic themselves and report it with
// t.Errorf, so a deleted guard fails by assertion.

// noPanic runs f and turns a panic into a test failure naming the guard.
func noPanic(t *testing.T, guard string, f func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s: panicked (%v) -- the guard that refuses first is gone", guard, r)
		}
	}()
	f()
}

// main.go run: `if code != 0 { return code }` after parse. A flag error must end
// the run there; -tally came BEFORE the bad flag, so a run that went on would
// print the count to stdout.
func TestARunWithABadFlagEndsAtTheFlag(t *testing.T) {
	noPanic(t, "run after a parse failure", func() {
		var out, errb bytes.Buffer
		code := run([]string{"-tally", "-dir", "testdata/edges", "-no-such-flag"}, &out, &errb)
		if code != 2 {
			t.Errorf("code=%d, want 2", code)
		}
		if out.Len() != 0 {
			t.Errorf("stdout %q: the run went on past a flag it had refused", out.String())
		}
	})
}

// guards.go parse: `len(cmd) == 0 && !cfg.tally`. No command and no -tally is
// a usage error, not a sweep of a suite that was never there.
func TestNoCommandWithoutTallyIsAUsageError(t *testing.T) {
	noPanic(t, "parse with no command", func() {
		var errb bytes.Buffer
		_, cmd, code := parse([]string{"-dir", "testdata/edges"}, &errb)
		if code != 2 || cmd != nil {
			t.Errorf("code=%d cmd=%v, want 2 and no command", code, cmd)
		}
		if !strings.Contains(errb.String(), "usage: mutsweep") {
			t.Errorf("stderr %q does not say how to use it", errb.String())
		}
		var out bytes.Buffer
		if code := run([]string{"-dir", "testdata/edges"}, &out, &errb); code != 2 || out.Len() != 0 {
			t.Errorf("run: code=%d stdout=%q, want 2 and nothing", code, out.String())
		}
	})
}

// sweep.go lastOf: `len(b) <= keep`. Output shorter than the window is returned
// whole; without the guard the slice index is negative.
func TestAShortOutputIsReturnedWhole(t *testing.T) {
	noPanic(t, "lastOf on short output", func() {
		if got := lastOf([]byte("  FAIL x  \n")); got != "FAIL x" {
			t.Errorf("lastOf(short) = %q, want %q", got, "FAIL x")
		}
		if got := lastOf(nil); got != "" {
			t.Errorf("lastOf(nil) = %q, want empty", got)
		}
	})
}

// guards.go boundaries: `if !ok { return true }`. A condition holds nodes that
// are not comparisons (identifiers, calls, selectors); each must be walked past,
// not read as one.
func TestBoundariesWalkPastNodesThatAreNotComparisons(t *testing.T) {
	src := "package p\n\nfunc f(x, y int) bool { return !g(x) && x < y.z }\n"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	cond := file.Decls[0].(*ast.FuncDecl).Body.List[0].(*ast.ReturnStmt).Results[0]
	noPanic(t, "boundaries over a non-comparison node", func() {
		got := boundaries(".", "p.go", fset, src, cond)
		if len(got) != 1 || !strings.Contains(got[0].cond, "[< to <=]") {
			t.Errorf("boundaries = %+v, want exactly the one `<` mutation", got)
		}
	})
}

// guards.go unique: `... || at == 0`. A span that is not unique even from the
// start of the file ends there; without the guard it indexes before the start.
func TestUniqueStopsAtTheStartOfTheFile(t *testing.T) {
	noPanic(t, "unique on a span that never becomes unique", func() {
		from, to := unique("<<", 0, 1)
		if from != "<" || to != "" {
			t.Errorf("unique = (%q, %q), want (%q, %q)", from, to, "<", "")
		}
		// The second line is only unique once widened to the first.
		from, to = unique("a<\na<", 4, 5)
		if from != "a<\na<" || to != "a<\na" {
			t.Errorf("unique = (%q, %q), want the two lines", from, to)
		}
	})
}
