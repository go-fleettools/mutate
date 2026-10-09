package main

import "testing"

// The output `go test` prints when a test FAILS and its own message happens to
// quote a phrase the compiler also uses. This is real: it is what go-odf
// printed on 2026-10-09, and it made a caught mutation read as a build
// failure.
const caughtButQuotingTheCompiler = `--- FAIL: TestAnEntryThatInflatesPastTheCeilingIsRefused (0.77s)
    limits_test.go:85: it was refused by *xml.SyntaxError (XML syntax error on line 6: unexpected EOF), not by the ceiling
--- FAIL: TestADeclaredSizeDecidesAndSaysSo (0.00s)
    limits_test.go:256: an entry one byte past the ceiling was accepted
FAIL
FAIL	github.com/go-odf/odf	1.882s
FAIL
`

// What the toolchain prints when a package really will not build: unindented,
// under a package header.
const reallyABuildFailure = `# github.com/go-odf/odf [github.com/go-odf/odf.test]
./limits_test.go:85:12: undefined: errTooLarge
FAIL	github.com/go-odf/odf [build failed]
`

const anotherRealOne = `# example/x
./x.go:9:2: declared and not used: n
`

func TestATestThatQuotesTheCompilerIsNotABuildFailure(t *testing.T) {
	// ⛔ The defect this exists for. "syntax error" appeared — inside a test's
	// own failure message, where it says something about the FILE under test
	// rather than about the package compiling. Searching the whole output
	// matched the quoted phrase and reported a mutation the suite had caught
	// as proving nothing.
	if looksLikeBuildFailure(caughtButQuotingTheCompiler) {
		t.Error("a failing test that quotes \"syntax error\" was read as a build failure: " +
			"a caught mutation then reads as one that proves nothing")
	}
}

func TestARealBuildFailureIsStillRecognised(t *testing.T) {
	// ⛔ The other side, and the side that matters more: a mutant that does not
	// compile is no test at all, and in a grep for FAIL it looks identical to a
	// caught one. Narrowing where the phrases count must not lose this.
	for _, out := range []string{reallyABuildFailure, anotherRealOne} {
		if !looksLikeBuildFailure(out) {
			t.Errorf("a real build failure was not recognised:\n%s", out)
		}
	}
}

func TestTheRunnersOwnSummaryStillCounts(t *testing.T) {
	// "[build failed]" is the test runner's word for this and arrives on a
	// line beginning FAIL, which is otherwise a line tests write.
	if !looksLikeBuildFailure("FAIL\tgithub.com/x/y [build failed]\n") {
		t.Error("the runner's own report of a build failure was missed")
	}
}

func TestWhetherTheCompilerCouldHaveWrittenALine(t *testing.T) {
	for _, c := range []struct {
		line string
		want bool
	}{
		{"./x.go:9:2: undefined: n", true},
		{"# example/x", true},
		{"    x_test.go:3: syntax error in the input we were handed", false},
		{"\tnested output", false},
		{"--- FAIL: TestX (0.01s)", false},
		{"=== RUN   TestX", false},
		{"ok  \texample/x\t0.1s", false},
		{"FAIL\texample/x\t0.2s", false},
		{"?   \texample/x\t[no test files]", false},
		{"PASS", false},
		{"", false},
	} {
		if got := compilerCouldHaveWritten(c.line); got != c.want {
			t.Errorf("%q: %v, want %v", c.line, got, c.want)
		}
	}
}
