package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Real output of a suite with one assertion failure (in a subtest) and one test
// that indexed past a table, captured from the go tool.
const plainBoth = `--- FAIL: TestAssert (0.00s)
    --- FAIL: TestAssert/sub (0.00s)
        x_test.go:7: bad
--- FAIL: TestPanic (0.00s)
panic: runtime error: index out of range [3] with length 3 [recovered, repanicked]

goroutine 7 [running]:
x.TestPanic(0x32b6c7fd66c8?)
	/src/x_test.go:10 +0x38
FAIL	x	0.054s
FAIL
`

const plainPanicOnly = `--- FAIL: TestPanic (0.00s)
panic: runtime error: index out of range [4] with length 4 [recovered, repanicked]

goroutine 7 [running]:
FAIL	x	0.054s
FAIL
`

const plainAssertOnly = `--- FAIL: TestAssert (0.00s)
    x_test.go:7: bad
    x_test.go:9: the log says panic: but only indented
FAIL
FAIL	x	0.003s
`

const jsonPanicOnly = `{"Action":"start","Package":"x"}
{"Action":"run","Package":"x","Test":"TestPanic"}
{"Action":"output","Package":"x","Test":"TestPanic","Output":"--- FAIL: TestPanic (0.00s)\n"}
{"Action":"output","Package":"x","Test":"TestPanic","Output":"panic: runtime error: index out of range [3] with length 3 [recovered, repanicked]\n"}
{"Action":"fail","Package":"x","Test":"TestPanic","Elapsed":0}
{"Action":"fail","Package":"x","Elapsed":0.152}
`

const jsonBoth = jsonPanicOnly + `{"Action":"run","Package":"x","Test":"TestAssert/sub"}
{"Action":"output","Package":"x","Test":"TestAssert/sub","Output":"    x_test.go:7: bad\n"}
{"Action":"fail","Package":"x","Test":"TestAssert/sub","Elapsed":0}
{"Action":"fail","Package":"x","Test":"TestAssert","Elapsed":0}
`

// The rule, against both readings of the same facts.
func TestClassifyTellsAnAssertionFromAPanic(t *testing.T) {
	for _, tt := range []struct {
		name string
		out  string
		json bool
		want verdict
	}{
		{"plain: a panic and an assertion", plainBoth, false, caught},
		{"plain: a panic alone", plainPanicOnly, false, caughtByPanic},
		{"plain: an assertion alone, quoting a panic indented", plainAssertOnly, false, caught},
		{"json: a panic alone", jsonPanicOnly, true, caughtByPanic},
		{"json: a panic and an assertion", jsonBoth, true, caught},
		// A red suite naming no failing test offers no evidence of luck.
		{"plain: red, but no test named", "FAIL\tx\t0.1s\nFAIL\n", false, caught},
		// A panic in a goroutine of its own ends the process before any test is
		// reported failed.
		{"plain: a panic outside any test", "panic: boom\n\ngoroutine 5 [running]:\nFAIL\tx\t0.1s\nFAIL\n", false, caughtByPanic},
		{"json: the plain reading answers when no event does", plainPanicOnly, true, caughtByPanic},
		// A parent fails because its subtest did; only the subtest counts.
		{"plain: a panicking subtest under a failing parent",
			"--- FAIL: TestA (0.00s)\n    --- FAIL: TestA/sub (0.00s)\npanic: boom\nFAIL\tx\t0.1s\nFAIL\n", false, caughtByPanic},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := classify([]byte(tt.out), tt.json)
			if got != tt.want {
				t.Fatalf("classified %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTheMethodIsNamed(t *testing.T) {
	if _, m := classify([]byte(jsonPanicOnly), true); m != byJSON {
		t.Errorf("events read as %q", m)
	}
	if _, m := classify([]byte(plainPanicOnly), false); m != byText {
		t.Errorf("text read as %q", m)
	}
	if _, m := classify([]byte(plainPanicOnly), true); m != byText {
		t.Errorf("a command that said -json but spoke text must be reported as read from text, got %q", m)
	}
}

func TestOnlyGoTestWithJSONIsReadAsEvents(t *testing.T) {
	for cmd, want := range map[string]bool{
		"go test -json ./...":             true,
		"go test -count 1 -json ./...":    true,
		"/usr/local/go/bin/go test -json": true,
		"go test ./...":                   false,
		"go vet -json ./...":              false,
		"make test -json":                 false,
		"go test -run X -- -json":         false,
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
	// The default is still deletion: one refusal, the second `if` having no
	// comparison worth moving is still a refusal to delete.
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
