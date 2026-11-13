package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// goTest runs the real go tool on one of the fixtures under testdata/review and
// returns what it printed. The fixtures are small packages whose outputs were
// captured from go1.27.4: a hand-written sample of the output missed every
// defect the first version of the classifier had, because it was written to
// match the classifier.
func goTest(t *testing.T, pkg string, flags ...string) ([]byte, []string) {
	t.Helper()
	dir, err := filepath.Abs("testdata/review")
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"test", "-count=1", "-timeout=3s"}, flags...)
	args = append(args, pkg)
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("go %v passed; a fixture is meant to be red:\n%s", args, out)
	}
	return out, append([]string{"go"}, args...)
}

// Each fixture, in each of the four shapes of output, against the rule: caught
// when any failing test printed an assertion line, caught by PANIC when there is
// a panic and no assertion line, whatever the test or package it came from.
func TestClassifyOnRealGoTestOutput(t *testing.T) {
	for _, c := range []struct {
		pkg  string
		want verdict
		why  string
	}{
		{"./sub", caughtByPanic, "a subtest panics: test2json files the panic under the PARENT"},
		{"./goroutine", caughtByPanic, "a panic in a goroutine the code started: no test-level fail event"},
		{"./timeout", caughtByPanic, "a go test -timeout expiry"},
		{"./glued", caughtByPanic, "a progress mark without a newline glued to the panic line"},
		{"./panicfirst", caughtByPanic, "the first panic kills the binary, so the asserting test never ran"},
		{"./assertthenpanic", caught, "a test asserts and then panics on the next line"},
		{"./nilmap", caught, "a subtest asserts before its parent panics"},
		{"./fatalthenpanic", caught, "one test asserts, another panics"},
		{"./printpanic", caught, "a passing test printed a line beginning panic:"},
		{"./recovered", caught, "the test recovered the panic and asserted on it"},
		// The one verdict that depends on the output mode (and, in general, on test
		// order): the panic ends the binary while the asserting test is still
		// running, and plain output without -v prints a test's lines only when it
		// finishes, so the assertion is lost. -v and -json stream it.
		{"./parallel -plain", caughtByPanic, "parallel tests, one asserts, one panics; plain output loses the assertion"},
		{"./parallel", caught, "parallel tests, one asserts, one panics; streamed output keeps it"},
		{"./multi/...", caught, "a.TestParse asserts; b.TestParse/edge panics: same name, two packages"},
	} {
		modes := [][]string{{}, {"-v"}, {"-json"}, {"-json", "-v"}}
		pkg := c.pkg
		if strings.HasSuffix(pkg, " -plain") {
			pkg, modes = strings.TrimSuffix(pkg, " -plain"), [][]string{{}}
		} else if pkg == "./parallel" {
			modes = modes[1:]
		}
		for _, flags := range modes {
			t.Run(pkg+" "+strings.Join(flags, " "), func(t *testing.T) {
				out, argv := goTest(t, pkg, flags...)
				got, _ := classify(out, asksForJSON(argv))
				if got != c.want {
					t.Fatalf("%s: classified %q, want %q\n%s", c.why, got, c.want, out)
				}
			})
		}
	}
}

// The rule on small inputs, for the corners real output is slow to reach and
// for the mutations of the readers.
func TestClassifyCorners(t *testing.T) {
	trace := "panic: boom\n\ngoroutine 7 [running]:\n"
	for _, tt := range []struct {
		name string
		out  string
		json bool
		want verdict
	}{
		{"plain: a panic with a trace and no assertion", "--- FAIL: TestA (0.00s)\n" + trace + "FAIL\tx\t0.1s\nFAIL\n", false, caughtByPanic},
		{"plain: red and nothing to point at", "FAIL\tx\t0.1s\nFAIL\n", false, caught},
		{"plain: a panic outside any test", trace + "FAIL\tx\t0.1s\nFAIL\n", false, caughtByPanic},
		{"plain: panic: with no trace is only text", "--- FAIL: TestA (0.00s)\npanic: boom\nFAIL\tx\t0.1s\nFAIL\n", false, caught},
		{"plain: an assertion of a failing test", "--- FAIL: TestA (0.00s)\n    x_test.go:7: bad\n" + trace + "FAIL\tx\t0.1s\n", false, caught},
		// The log of a PASSING test is not an assertion: with -v it prints the
		// same `file.go:N:` form.
		{"plain -v: a passing test's log is not an assertion",
			"=== RUN   TestA\n    x_test.go:7: just logging\n--- PASS: TestA (0.00s)\n=== RUN   TestB\n--- FAIL: TestB (0.00s)\n" + trace + "FAIL\tx\t0.1s\n", false, caughtByPanic},
		// A package ends at its own summary line: the next one's lines are not
		// the first one's, even for a test of the same name.
		{"plain: two packages, same test name",
			"--- FAIL: TestP (0.00s)\n    a_test.go:5: bad\nFAIL\ta\t0.1s\n--- FAIL: TestP/edge (0.00s)\n" + trace + "FAIL\tb\t0.1s\nFAIL\n", false, caught},
		{"plain: an ok line ends a package too",
			"=== RUN   TestP\n    a_test.go:5: logged by a green package\n--- PASS: TestP (0.00s)\nok  \ta\t0.1s\n--- FAIL: TestP (0.00s)\n" + trace + "FAIL\tb\t0.1s\n", false, caughtByPanic},
		{"json: no event answers, the text does", "--- FAIL: TestA (0.00s)\n" + trace + "FAIL\tx\t0.1s\n", true, caughtByPanic},
		{"json: a panic in the output of a package, not a test",
			`{"Action":"output","Package":"x","Output":"panic: boom\n"}` + "\n" +
				`{"Action":"output","Package":"x","Output":"\n"}` + "\n" +
				`{"Action":"output","Package":"x","Output":"goroutine 7 [running]:\n"}` + "\n" +
				`{"Action":"fail","Package":"x"}` + "\n", true, caughtByPanic},
		{"json: a failing test's assertion in another package",
			`{"Action":"output","Package":"a","Test":"TestP","Output":"    a_test.go:5: bad\n"}` + "\n" +
				`{"Action":"fail","Package":"a","Test":"TestP"}` + "\n" +
				`{"Action":"output","Package":"b","Test":"TestP","Output":"panic: boom\n"}` + "\n" +
				`{"Action":"output","Package":"b","Test":"TestP","Output":"goroutine 7 [running]:\n"}` + "\n" +
				`{"Action":"fail","Package":"b","Test":"TestP"}` + "\n", true, caught},
		{"json: an assertion line of a test that did not fail does not count",
			`{"Action":"output","Package":"a","Test":"TestP","Output":"    a_test.go:5: logged\n"}` + "\n" +
				`{"Action":"pass","Package":"a","Test":"TestP"}` + "\n" +
				`{"Action":"output","Package":"a","Test":"TestQ","Output":"panic: boom\n"}` + "\n" +
				`{"Action":"output","Package":"a","Test":"TestQ","Output":"goroutine 7 [running]:\n"}` + "\n" +
				`{"Action":"fail","Package":"a","Test":"TestQ"}` + "\n", true, caughtByPanic},
		{"json: lines that are not events are skipped",
			"# a build note\n" +
				`{"Action":"output","Package":"a","Test":"TestP","Output":"    a_test.go:5: bad\n"}` + "\n" +
				"not json {\n" +
				`{"Action":"fail","Package":"a","Test":"TestP"}` + "\n", true, caught},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := classify([]byte(tt.out), tt.json)
			if got != tt.want {
				t.Fatalf("classified %q, want %q", got, tt.want)
			}
		})
	}
}

// The edges of the panic window, asserted. A boundary sweep of this very file
// found `j < len(lines)` moved to `<=` held ONLY by an index-out-of-range panic:
// the tool's own verdict, on its own code.
func TestAPanicNeedsATraceWithinTheWindowAndNotPastTheEnd(t *testing.T) {
	mk := func(filler int, trace bool) []outLine {
		ls := []outLine{{text: "panic: boom"}}
		for i := 1; i < filler; i++ {
			ls = append(ls, outLine{text: "x"})
		}
		if trace {
			ls = append(ls, outLine{text: "goroutine 7 [running]:"})
		}
		return ls
	}
	for _, tt := range []struct {
		name string
		in   []outLine
		want bool
	}{
		{"the panic line is the last line", mk(1, false), false},
		{"the trace is the very next line", mk(1, true), true},
		{"the trace is exactly panicWindow lines after", mk(panicWindow, true), true},
		{"the trace is one line past the window", mk(panicWindow+1, true), false},
		{"the trace is the last line of the output", mk(5, true), true},
		{"a goroutine line that is not a header", []outLine{{text: "panic: boom"}, {text: "goroutine 7"}}, false},
		{"a header that is not a goroutine line", []outLine{{text: "panic: boom"}, {text: "other [running]:"}}, false},
	} {
		if got := hasPanic(tt.in); got != tt.want {
			t.Errorf("%s: hasPanic = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestTheMethodIsNamed(t *testing.T) {
	ev := `{"Action":"fail","Package":"x","Test":"TestA"}` + "\n"
	if _, m := classify([]byte(ev), true); m != byJSON {
		t.Errorf("events read as %q", m)
	}
	if _, m := classify([]byte("--- FAIL: TestA\n"), false); m != byText {
		t.Errorf("text read as %q", m)
	}
	if _, m := classify([]byte("--- FAIL: TestA\n"), true); m != byText {
		t.Errorf("a command that said -json but spoke text must be reported as read from text, got %q", m)
	}
}

func TestOnlyGoTestWithJSONIsReadAsEvents(t *testing.T) {
	for cmd, want := range map[string]bool{
		"go test -json ./...":             true,
		"go test -count 1 -json ./...":    true,
		"/usr/local/go/bin/go test -json": true,
		"go test -json=true ./...":        true,
		"go test ./...":                   false,
		"go vet -json ./...":              false,
		"make test -json":                 false,
		"go test -run X -- -json":         false,
		"go":                              false,
	} {
		if got := asksForJSON(strings.Fields(cmd)); got != want {
			t.Errorf("%q: asksForJSON = %v, want %v", cmd, got, want)
		}
	}
}

// A panic red needs reading and fails a CI gate, like a survivor does.
func TestAPanicRedFailsTheSweepAndIsListed(t *testing.T) {
	var out, errOut strings.Builder
	rs := []result{{verdict: caught}, {guard: guard{file: "kn.go", line: 66, cond: "x >= 512  [>= to >]"}, verdict: caughtByPanic}}
	if code := report(rs, time.Minute, &out, &errOut); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(out.String(), "1 caught, 1 caught by PANIC") {
		t.Errorf("the panic is not counted apart:\n%s", out.String())
	}
	if !strings.Contains(errOut.String(), "what nothing holds:") || !strings.Contains(errOut.String(), "caught by PANIC") ||
		!strings.Contains(errOut.String(), "kn.go:66") {
		t.Errorf("the panic is not listed under what nothing holds:\n%s", errOut.String())
	}
}

// ⛔ A mutate that does not know -echo rejects every mutant with exit status 2,
// the same status as "did not compile", and a gate that ran nothing exited 0.
func TestAnOldMutateIsRefusedBeforeAnythingRuns(t *testing.T) {
	dir := t.TempDir()
	old := buildOldMutate(t)
	write(t, dir, "go.mod", "module x\n\ngo 1.27.1\n")
	write(t, dir, "x.go", "package x\n\nfunc F(n int) int {\n\tif n < 0 {\n\t\treturn 0\n\t}\n\treturn n\n}\n")
	var out, errOut strings.Builder
	code := run([]string{"-dir", dir, "-mutate-bin", old, "--", "true"}, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit %d, want 2:\n%s%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "-echo") {
		t.Errorf("the refusal does not name the flag: %q", errOut.String())
	}
	if strings.Contains(out.String(), "refusals,") {
		t.Errorf("a sweep started: %q", out.String())
	}

	// And one that slips past the check is still a failing gate, never a pass.
	g := oneGuardOf(t, dir)
	got := one(config{dir: dir, timeout: time.Minute, mutate: old}, g[0], []string{"true"})
	if got.verdict != unrun {
		t.Fatalf("verdict %q (%s), want %q", got.verdict, got.note, unrun)
	}
	if code := report([]result{got}, time.Minute, &out, &errOut); code != 1 {
		t.Fatalf("report exit %d, want 1", code)
	}
}

// buildOldMutate compiles a stand-in that behaves as a mutate from before -echo:
// the same flags but that one, and exit 2 on any other, as the flag package does.
func buildOldMutate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	const prog = `package main

import (
	"flag"
	"os"
)

func main() {
	fs := flag.NewFlagSet("mutate", flag.ContinueOnError)
	fs.String("file", "", "")
	fs.String("from", "", "")
	fs.String("to", "", "")
	fs.String("name", "", "")
	fs.Bool("expect-pass", false, "")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
}
`
	write(t, dir, "main.go", prog)
	write(t, dir, "go.mod", "module oldmutate\n\ngo 1.27.1\n")
	bin := filepath.Join(t.TempDir(), "oldmutate"+exeSuffix())
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the stand-in: %v\n%s", err, out)
	}
	return bin
}

// Four guards, four ways of being held, four verdicts -- through the real mutate
// and the real go tool, on a copy so the fixture is never the thing mutated.
func TestEachBoundaryGetsItsOwnVerdict(t *testing.T) {
	mutateBin := buildMutate(t)
	for _, cmd := range [][]string{
		{"go", "test", "-count", "1", "-json", "./..."},
		{"go", "test", "-count", "1", "./..."},
	} {
		t.Run(strings.Join(cmd[3:], " "), func(t *testing.T) {
			dir := t.TempDir()
			if err := os.CopyFS(dir, os.DirFS("testdata/edges")); err != nil {
				t.Fatal(err)
			}
			guards, _, err := collect(config{dir: dir, mode: modeBoundary})
			if err != nil {
				t.Fatal(err)
			}
			if len(guards) != 4 {
				t.Fatalf("found %d comparisons, want 4: %+v", len(guards), guards)
			}
			want := map[string]verdict{
				"n > 9":   caught,
				"x >= 4":  caughtByPanic,
				"n >= 1":  hung,
				"x > 100": survived,
			}
			for _, g := range guards {
				timeout := 2 * time.Minute
				if strings.HasPrefix(g.cond, "n >= 1") {
					timeout = 3 * time.Second
				}
				got := one(config{dir: dir, timeout: timeout, mutate: mutateBin, mode: modeBoundary}, g, cmd)
				key := strings.TrimSpace(g.cond[:strings.Index(g.cond, "  [")])
				if w, ok := want[key]; !ok || got.verdict != w {
					t.Errorf("%s: verdict %q (%s), want %q", g.cond, got.verdict, got.note, w)
				}
			}
		})
	}
}

func TestBoundaryMovesEveryComparisonAndNothingElse(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module x\n\ngo 1.27.1\n")
	write(t, dir, "x.go", `package x

func F(a, b, c int, s []int) int {
	if a < b && b >= c {
		return 1
	}
	if a == b || a != c {
		return 2
	}
	for i := 0; i <= len(s); i++ {
		_ = i
	}
	for range s {
	}
	for i := len(s); i > 0; i-- {
		_ = i
	}
	return 0
}
`)
	got, _, err := collect(config{dir: dir, mode: modeBoundary})
	if err != nil {
		t.Fatal(err)
	}
	var conds []string
	for _, g := range got {
		conds = append(conds, g.cond)
	}
	want := []string{
		"a < b  [< to <=]", "b >= c  [>= to >]",
		"i <= len(s)  [<= to <]", "i > 0  [> to >=]",
	}
	if strings.Join(conds, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q\nwant %q", conds, want)
	}
	// The default is still deletion: the two refusals, whatever their operators.
	del, _, err := collect(config{dir: dir})
	if err != nil || len(del) != 2 {
		t.Fatalf("deletion found %d refusals (%v), want 2", len(del), err)
	}
}

// Two identical guards are both addressable: each mutation is widened until it
// occurs once, so mutate does not refuse it as ambiguous.
func TestIdenticalComparisonsAreStillAddressable(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module x\n\ngo 1.27.1\n")
	write(t, dir, "x.go", `package x

func A(n int) int {
	if n < 3 {
		return 1
	}
	return 0
}

func B(n int) int {
	if n < 3 {
		return 1
	}
	return 0
}
`)
	got, _, err := collect(config{dir: dir, mode: modeBoundary})
	if err != nil || len(got) != 2 {
		t.Fatalf("found %d (%v), want 2", len(got), err)
	}
	src, _ := os.ReadFile(dir + "/x.go")
	for _, g := range got {
		if n := strings.Count(string(src), g.from); n != 1 {
			t.Errorf("line %d: from occurs %d times, want 1", g.line, n)
		}
		if g.to == g.from || !strings.HasSuffix(g.to, "<=") {
			t.Errorf("line %d: to %q does not move the edge of %q", g.line, g.to, g.from)
		}
	}
}

func TestOnlyNamesAComparisonByItsLine(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module x\n\ngo 1.27.1\n")
	write(t, dir, "x.go", `package x

func F(a, b, c int) int {
	if a < b && b >= c {
		return 1
	}
	return 0
}
`)
	got, _, err := collect(config{dir: dir, mode: modeBoundary, only: []target{{"x.go", 4}}})
	if err != nil || len(got) != 2 {
		t.Fatalf("a line holding two comparisons gave %d (%v), want both", len(got), err)
	}
}

func TestAMutateModeThatIsNeitherIsRefused(t *testing.T) {
	var errOut strings.Builder
	if _, _, code := parse([]string{"-mutate", "mutate", "--", "true"}, &errOut); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "-mutate-bin") {
		t.Errorf("the refusal does not say where the command went: %q", errOut.String())
	}
}
