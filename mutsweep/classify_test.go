package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// goTest runs the real go tool on a fixture package and returns what it printed.
// The fixtures are small packages run against go1.27.4: a hand-written sample of
// the output missed every defect the first versions of the classifier had,
// because it was written to match the classifier.
func goTest(t *testing.T, fixtures, pkg string, flags ...string) ([]byte, []string) {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("testdata", fixtures))
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

// Each fixture, in each shape of output, against the rule: caught by PANIC when
// there is a panic and no test that did not panic printed an assertion line;
// ambiguity resolves to PANIC.
func TestClassifyOnRealGoTestOutput(t *testing.T) {
	all := [][]string{{}, {"-v"}, {"-json"}, {"-json", "-v"}}
	streamed := all[1:]
	for _, c := range []struct {
		fixtures, pkg string
		modes         [][]string
		want          verdict
		why           string
	}{
		// review: round one.
		{"review", "./sub", all, caughtByPanic, "a subtest panics: test2json files the panic under the PARENT"},
		{"review", "./goroutine", all, caughtByPanic, "a panic in a goroutine the code started: no test-level fail event"},
		{"review", "./timeout", all, caughtByPanic, "a go test -timeout expiry"},
		{"review", "./glued", all, caughtByPanic, "a progress mark without a newline glued to the panic line"},
		{"review", "./panicfirst", all, caughtByPanic, "the first panic kills the binary, so the asserting test never ran"},
		{"review", "./assertthenpanic", all, caughtByPanic, "asserts then panics: its lines cannot be told from t.Log, so PANIC"},
		// Where the output files the parent's panic under the SUBTEST that ran last
		// (plain, -v and -json -v do), the subtest's assertion cannot be told from
		// its panic's neighbour: a false alarm, never the reverse.
		{"review", "./nilmap", [][]string{{"-json"}}, caught, "a subtest asserts before its parent panics; the panic is filed under the parent"},
		{"review", "./nilmap", [][]string{{}, {"-v"}, {"-json", "-v"}}, caughtByPanic, "the same, with the panic filed under the subtest: ambiguity resolves to PANIC"},
		{"review", "./fatalthenpanic", all, caught, "one test asserts, another panics"},
		{"review", "./printpanic", all, caught, "a passing test printed a line beginning panic:"},
		{"review", "./recovered", all, caught, "the test recovered the panic and asserted on it"},
		{"review", "./multi/...", all, caught, "a.TestParse asserts; b.TestParse/edge panics: same name, two packages"},
		{"review", "./parallel", streamed, caught, "parallel tests, one asserts, one panics; streamed output keeps it"},
		{"review", "./parallel", all[:1], caughtByPanic, "plain output loses the assertion of a test still running: a false alarm, never the reverse"},
		// review3: round two. Every one of these was a panic certified as caught.
		{"review3", "./tlog", all, caughtByPanic, "t.Logf then a panic: text cannot tell t.Log from t.Error"},
		{"review3", "./sublog", all, caughtByPanic, "the parent logs, only its subtest panics"},
		{"review3", "./multiline", all, caughtByPanic, "a panic message of twenty lines: the trace is not within a window"},
		{"review3", "./bigline", all, caughtByPanic, "a 100 KB panic line, delivered in fragments by test2json"},
		{"review3", "./framepanic", all, caughtByPanic, "a panic value that prints like an assertion line"},
		{"review3", "./stackdump", streamed, caught, "a PASSING test printed panic: and a stack; the other test fails with no message"},
		{"review3", "./stackdump", all[:1], caughtByPanic, "plain output cannot tell whose stack dump it is: a false alarm"},
	} {
		for _, flags := range c.modes {
			t.Run(c.fixtures+" "+c.pkg+" "+strings.Join(flags, " "), func(t *testing.T) {
				out, argv := goTest(t, c.fixtures, c.pkg, flags...)
				got, _ := classify(out, asksForJSON(argv))
				if got != c.want {
					t.Fatalf("%s: classified %q, want %q\n%.3000s", c.why, got, c.want, out)
				}
			})
		}
	}
}

const trace = "panic: boom\n\ngoroutine 7 [running]:\n"

// ev builds one test2json event line.
func ev(action, pkg, test, output string) string {
	b, err := json.Marshal(event{Action: action, Package: pkg, Test: test, Output: output})
	if err != nil {
		panic(err)
	}
	return string(b) + "\n"
}

// The rule on small inputs: the corners real output is slow to reach, and one
// case for each mutation of the readers that needs to be killed by assertion.
func TestClassifyCorners(t *testing.T) {
	long := strings.Repeat("a joined error line\n", 20)
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
		{"plain: another test's assertion", "--- FAIL: TestA (0.00s)\n    x_test.go:7: bad\n--- FAIL: TestB (0.00s)\n" + trace + "FAIL\tx\t0.1s\n", false, caught},
		{"plain: the test that panicked printed it", "--- FAIL: TestA (0.00s)\n    x_test.go:7: bad\n" + trace + "FAIL\tx\t0.1s\n", false, caughtByPanic},
		{"plain: a panic message of twenty lines", "--- FAIL: TestA (0.00s)\npanic: " + long + "\ngoroutine 7 [running]:\nFAIL\tx\t0.1s\n", false, caughtByPanic},

		// What a test that PASSED printed is not an assertion (go test -v shows it).
		{"plain -v: a passing test's log",
			"=== RUN   TestA\n    x_test.go:7: just logging\n--- PASS: TestA (0.00s)\n=== RUN   TestB\n--- FAIL: TestB (0.00s)\n" + trace + "FAIL\tx\t0.1s\n", false, caughtByPanic},
		// And a line belongs to the test of the `=== RUN` above it.
		{"plain -v: a log under its own RUN line, after another test's FAIL line",
			"--- FAIL: TestB (0.00s)\n=== RUN   TestA\n    x_test.go:7: log of A\n--- PASS: TestA (0.00s)\n=== RUN   TestC\n--- FAIL: TestC (0.00s)\n" + trace + "FAIL\tx\t0.1s\n", false, caughtByPanic},
		{"plain -v: a log under =CONT of a failing test",
			"=== RUN   TestA\n=== CONT  TestA\n    x_test.go:7: bad\n--- FAIL: TestA (0.00s)\n=== RUN   TestC\n--- FAIL: TestC (0.00s)\n" + trace + "FAIL\tx\t0.1s\n", false, caught},
		{"plain: two packages, same test name",
			"--- FAIL: TestP (0.00s)\n    a_test.go:5: bad\nFAIL\ta\t0.1s\n--- FAIL: TestP/edge (0.00s)\n" + trace + "FAIL\tb\t0.1s\nFAIL\n", false, caught},
		{"plain: an ok line ends a package too",
			"=== RUN   TestP\n    a_test.go:5: logged by a green package\n--- PASS: TestP (0.00s)\nok  \ta\t0.1s\n--- FAIL: TestP (0.00s)\n" + trace + "FAIL\tb\t0.1s\n", false, caughtByPanic},

		// The trace header is a goroutine line with a state in brackets, ending ]:.
		{"header: no bracket", "--- FAIL: TestA (0.00s)\npanic: boom\ngoroutine 7\nFAIL\tx\t0.1s\n", false, caught},
		{"header: not a goroutine line", "--- FAIL: TestA (0.00s)\npanic: boom\nother [running]:\nFAIL\tx\t0.1s\n", false, caught},
		{"header: no colon", "--- FAIL: TestA (0.00s)\npanic: boom\ngoroutine 7 [running]\nFAIL\tx\t0.1s\n", false, caught},
		{"header: a state without its opening bracket", "--- FAIL: TestA (0.00s)\npanic: boom\ngoroutine 7 running]:\nFAIL\tx\t0.1s\n", false, caught},
		{"header: a goroutine state other than running", "--- FAIL: TestA (0.00s)\npanic: boom\ngoroutine 7 [chan receive]:\nFAIL\tx\t0.1s\n", false, caughtByPanic},

		{"json: no event answers, the text does", "--- FAIL: TestA (0.00s)\n" + trace + "FAIL\tx\t0.1s\n", true, caughtByPanic},
		{"json: lines that are not events are skipped",
			"# a build note\n" + ev("output", "a", "TestP", "    a_test.go:5: bad\n") + "not json {\n" + ev("fail", "a", "TestP", ""), true, caught},
		{"json: a panic in the output of a package, not a test, kills the package",
			ev("output", "x", "TestP", "    a_test.go:5: bad\n") + ev("fail", "x", "TestP", "") +
				ev("output", "x", "", "panic: boom\n") + ev("output", "x", "", "goroutine 7 [running]:\n") + ev("fail", "x", "", ""), true, caughtByPanic},
		{"json: but another package's assertion still counts",
			ev("output", "x", "", "panic: boom\n") + ev("output", "x", "", "goroutine 7 [running]:\n") +
				ev("output", "y", "TestQ", "    q_test.go:5: bad\n") + ev("fail", "y", "TestQ", ""), true, caught},
		{"json: an assertion in another package, same test name",
			ev("output", "a", "TestP", "    a_test.go:5: bad\n") + ev("fail", "a", "TestP", "") +
				ev("output", "b", "TestP", "panic: boom\n") + ev("output", "b", "TestP", "goroutine 7 [running]:\n") + ev("fail", "b", "TestP", ""), true, caught},
		{"json: an assertion line of a test that PASSED does not count",
			ev("output", "a", "TestP", "    a_test.go:5: logged\n") + ev("pass", "a", "TestP", "") +
				ev("output", "a", "TestQ", "panic: boom\n") + ev("output", "a", "TestQ", "goroutine 7 [running]:\n") + ev("fail", "a", "TestQ", ""), true, caughtByPanic},
		{"json: a stack dump printed by a test that PASSED is not a panic",
			ev("output", "a", "TestDump", "panic: recovered by the worker pool\n") + ev("output", "a", "TestDump", "goroutine 1 [running]:\n") +
				ev("pass", "a", "TestDump", "") + ev("fail", "a", "TestFail", "") + ev("fail", "a", "", ""), true, caught},
		{"json: a skipped test's stack dump is not a panic either",
			ev("output", "a", "TestDump", "panic: x\n") + ev("output", "a", "TestDump", "goroutine 1 [running]:\n") +
				ev("skip", "a", "TestDump", "") + ev("fail", "a", "TestFail", ""), true, caught},

		// A panic is filed under the parent; the parent and its ancestors are tainted,
		// a sibling subtest and a test whose name merely starts the same are not.
		{"json: the parent's log, the subtest's panic",
			ev("output", "a", "TestS", "    s_test.go:5: case 4\n") + ev("output", "a", "TestS/b", "panic: boom\n") +
				ev("output", "a", "TestS/b", "goroutine 7 [running]:\n") + ev("fail", "a", "TestS/b", "") + ev("fail", "a", "TestS", ""), true, caughtByPanic},
		{"json: a grandparent's log",
			ev("output", "a", "TestS", "    s_test.go:5: case 4\n") + ev("output", "a", "TestS/b/c", "panic: boom\n") +
				ev("output", "a", "TestS/b/c", "goroutine 7 [running]:\n") + ev("fail", "a", "TestS/b/c", ""), true, caughtByPanic},
		{"json: a sibling subtest's assertion",
			ev("output", "a", "TestS/a", "    s_test.go:5: bad\n") + ev("fail", "a", "TestS/a", "") + ev("output", "a", "TestS/b", "panic: boom\n") +
				ev("output", "a", "TestS/b", "goroutine 7 [running]:\n") + ev("fail", "a", "TestS/b", ""), true, caught},
		{"json: a test whose name merely begins like the parent's",
			ev("output", "a", "TestSX", "    s_test.go:5: bad\n") + ev("fail", "a", "TestSX", "") + ev("output", "a", "TestS/b", "panic: boom\n") +
				ev("output", "a", "TestS/b", "goroutine 7 [running]:\n") + ev("fail", "a", "TestS/b", ""), true, caught},

		// Fragments of one line, joined.
		{"json: a panic line split in two events",
			ev("output", "a", "TestP", "pa") + `{"Action":"output","Package":"a","Test":"TestP","Output":"nic: boom\n"}` + "\n" +
				ev("output", "a", "TestP", "goroutine 7 [running]:\n") + ev("fail", "a", "TestP", ""), true, caughtByPanic},
		{"json: the trace header split in two events",
			ev("output", "a", "TestP", "panic: boom\n") + `{"Action":"output","Package":"a","Test":"TestP","Output":"gorou"}` + "\n" +
				`{"Action":"output","Package":"a","Test":"TestP","Output":"tine 7 [running]:\n"}` + "\n" + ev("fail", "a", "TestP", ""), true, caughtByPanic},
		{"json: a last line with no newline is still a line",
			ev("output", "a", "TestP", "panic: boom\n") + `{"Action":"output","Package":"a","Test":"TestP","Output":"goroutine 7 [running]:"}` + "\n" + ev("fail", "a", "TestP", ""), true, caughtByPanic},
		{"json: fragments of two tests are not mixed",
			`{"Action":"output","Package":"a","Test":"TestP","Output":"    a_test.go:5: "}` + "\n" +
				`{"Action":"output","Package":"a","Test":"TestQ","Output":"panic: boom\n"}` + "\n" +
				`{"Action":"output","Package":"a","Test":"TestP","Output":"bad\n"}` + "\n" +
				ev("output", "a", "TestQ", "goroutine 7 [running]:\n") + ev("fail", "a", "TestP", "") + ev("fail", "a", "TestQ", ""), true, caught},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := classify([]byte(tt.out), tt.json)
			if got != tt.want {
				t.Fatalf("classified %q, want %q", got, tt.want)
			}
		})
	}
}

// The note on a plain-output PANIC says how to confirm it.
func TestAPlainPanicVerdictSaysToRerunWithJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("testdata/edges")); err != nil {
		t.Fatal(err)
	}
	guards, _, err := collect(config{dir: dir, mode: modeBoundary})
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range guards {
		if !strings.HasPrefix(g.cond, "x >= 4") {
			continue
		}
		mb := buildMutate(t)
		plain := one(config{dir: dir, timeout: 2 * time.Minute, mutate: mb, mode: modeBoundary}, g, []string{"go", "test", "-count", "1", "./..."})
		if plain.verdict != caughtByPanic || !strings.Contains(plain.note, "-json") {
			t.Errorf("plain: %q %q, want a PANIC naming -json", plain.verdict, plain.note)
		}
		js := one(config{dir: dir, timeout: 2 * time.Minute, mutate: mb, mode: modeBoundary}, g, []string{"go", "test", "-count", "1", "-json", "./..."})
		if js.verdict != caughtByPanic || strings.Contains(js.note, "re-run") {
			t.Errorf("json: %q %q, want a PANIC without the plain caveat", js.verdict, js.note)
		}
	}
}
