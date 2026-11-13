package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// A red suite is not yet a held guard.
//
// ⛔ "THE SUITE WENT RED" HAS TWO CAUSES, AND ONLY ONE OF THEM IS A TEST. A test
// that asserts fails because it compared a value with the one it expected. A test
// that merely walks past the guard fails because the code after it indexed past
// an array, dereferenced nil, or divided by zero -- a runtime panic that no
// author wrote an expectation for. Twice in one day in go-numerics/special the
// only thing that made a mutated range guard red was such a panic
// (`x < 512` to `x <= 512` failed only with `index out of range` at
// kn_fast.go:66), and a sweep that counts any red as caught reported both as
// held. A panic red is luck: the next change that enlarges the table makes the
// mutant survive, silently.
//
// The rule, in one sentence: a red suite is CAUGHT when any failing test, at any
// level, printed an assertion line; it is CAUGHT BY PANIC when the output holds a
// panic and no failing test printed an assertion line anywhere.
//
// It is written that way, and not as "every failing leaf test panicked", because
// the first version was wrong in both directions on real go1.27.4 output:
//
//   - test2json files a subtest's panic under the PARENT test, not the leaf, so
//     dropping parents as "not leaves" dropped the only test that panicked;
//   - a panic with no test-level fail event (a `go test -timeout` expiry, a panic
//     in a goroutine the code under test started) named no failing test at all;
//   - a test that asserted and THEN panicked on the next line was read as luck,
//     when its assertion is exactly what holds the guard;
//   - a passing test that printed a line beginning `panic:` flipped the verdict;
//   - plain output across packages attached no package, so one package's
//     assertion was hidden behind another package's panic of the same test name.
//
// ⛔ THE VERDICT DEPENDS ON WHICH TESTS RAN. The first panic ends its test
// binary, so a test that would have asserted afterwards never runs, and the
// verdict can differ with test order (-shuffle, -run, a different package order).
// A panic red is reported as luck because the assertion it needs may simply not
// have been reached; run the failing test alone to ask.

// how says which way a red suite was read, because the two are not equally
// exact and the report has to say which one it used.
type how string

const (
	// byJSON reads the `go test -json` events, where each line of output is
	// already attributed to the package and test that printed it.
	byJSON how = "go test -json events"
	// byText reads the plain output, attributing a line to the last test named
	// by a `=== RUN`/`--- FAIL:` line before it, within one package. It is a
	// reading of prose, and says so.
	byText how = "the plain output"
)

// asksForJSON says whether the command is `go test` run with -json, in which
// case its output is a stream of events rather than text.
func asksForJSON(cmd []string) bool {
	if len(cmd) < 2 || strings.TrimSuffix(filepath.Base(cmd[0]), ".exe") != "go" || cmd[1] != "test" {
		return false
	}
	for _, a := range cmd[2:] {
		if a == "--" {
			return false
		}
		if a == "-json" || a == "--json" || a == "-json=true" || a == "--json=true" {
			return true
		}
	}
	return false
}

// outLine is one line of a suite's output with the test that printed it.
type outLine struct {
	pkg, test, text string
}

type testKey struct{ pkg, test string }

// assertionLine is what t.Error, t.Fatal and t.Log print: a source position,
// indented. A stack frame is a path followed by ` +0x`, never `: `.
var assertionLine = regexp.MustCompile(`^\s+[^\s:]+\.go:\d+: `)

// panicWindow is how far after a `panic:` line the runtime's `goroutine N [`
// header has to appear for the line to be a real panic. A passing test that
// prints the word, or a subprocess's stderr copied through, has no trace.
const panicWindow = 12

// classify reads what a red suite said and returns caught or caughtByPanic.
//
// Nothing found to point at is CAUGHT, never panic: the claim that a guard is
// held only by luck needs a panic to show.
func classify(out []byte, asJSON bool) (verdict, how) {
	method := byText
	var lines []outLine
	var passed map[testKey]bool
	if asJSON {
		method = byJSON
		var ok bool
		if lines, passed, ok = readEvents(out); !ok {
			// No event at all: whatever ran did not speak JSON, so say that the
			// plain reading is what answered.
			method = byText
			lines, passed = readText(out)
		}
	} else {
		lines, passed = readText(out)
	}
	return decide(lines, passed), method
}

// decide is the rule itself, kept apart from both readers.
//
// An assertion line counts unless its test is known to have PASSED. It is "not
// passed", and not "failed", because a test still running when a panic ends the
// binary never gets a fail event, and its assertion is no less one for that.
// What a passing test logs (go test -v prints it) is not an assertion.
func decide(lines []outLine, passed map[testKey]bool) verdict {
	for _, l := range lines {
		if !passed[testKey{l.pkg, l.test}] && assertionLine.MatchString(l.text) {
			return caught
		}
	}
	if hasPanic(lines) {
		return caughtByPanic
	}
	return caught
}

// hasPanic is whether any line is the Go runtime's report of a panic: a line
// holding `panic:` that is followed by a goroutine trace. The trace is the
// evidence, not the position in the line: a test that prints a progress mark
// without a newline glues it to the front of the real report (`.panic: ...`),
// while an assertion quoting a panic, or a passing test printing the word, has
// no trace after it.
func hasPanic(lines []outLine) bool {
	for i, l := range lines {
		if !strings.Contains(l.text, "panic:") {
			continue
		}
		for j := i + 1; j < len(lines) && j <= i+panicWindow; j++ {
			if strings.HasPrefix(lines[j].text, "goroutine ") && strings.Contains(lines[j].text, " [") {
				return true
			}
		}
	}
	return false
}

// event is the part of a test2json event this reads.
type event struct {
	Action  string
	Package string
	Test    string
	Output  string
}

func readEvents(out []byte) (lines []outLine, passed map[testKey]bool, ok bool) {
	passed = map[testKey]bool{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 || raw[0] != '{' {
			continue
		}
		var e event
		if err := json.Unmarshal(raw, &e); err != nil || e.Action == "" {
			continue
		}
		ok = true
		switch e.Action {
		case "output":
			for _, text := range strings.Split(strings.TrimSuffix(e.Output, "\n"), "\n") {
				lines = append(lines, outLine{e.Package, e.Test, text})
			}
		case "pass", "skip":
			if e.Test != "" {
				passed[testKey{e.Package, e.Test}] = true
			}
		}
	}
	return lines, passed, ok
}

// readText attributes each line to the test named by the last `=== RUN`,
// `=== NAME`, `=== CONT` or result line before it, inside one package; a
// package ends at its own `ok` or `FAIL` line.
//
// The go tool prints a failing test's block as
//
//	--- FAIL: TestX (0.00s)
//	    x_test.go:7: bad
//
// and with -v streams the same lines under `=== RUN`, so both attach to X.
func readText(out []byte) (lines []outLine, passed map[testKey]bool) {
	passed = map[testKey]bool{}
	pkg, test := "p0", ""
	n := 0
	for _, text := range strings.Split(string(out), "\n") {
		trimmed := strings.TrimLeft(text, " \t")
		switch {
		case strings.HasPrefix(trimmed, "--- FAIL: "):
			test = firstField(strings.TrimPrefix(trimmed, "--- FAIL: "))
		case strings.HasPrefix(trimmed, "--- "):
			// PASS and SKIP: the lines around it belong to a test that did not fail.
			test = firstField(trimmed[strings.IndexByte(trimmed, ':')+1:])
			passed[testKey{pkg, test}] = true
		case strings.HasPrefix(text, "=== "):
			f := strings.Fields(text)
			if len(f) >= 3 {
				test = f[2]
			}
		case endsPackage(text):
			// The summary line is the package's own, and the next lines are the
			// next package's.
			lines = append(lines, outLine{pkg, "", text})
			n++
			pkg, test = "p"+strconv.Itoa(n), ""
			continue
		}
		lines = append(lines, outLine{pkg, test, text})
	}
	return lines, passed
}

func endsPackage(line string) bool {
	return strings.HasPrefix(line, "FAIL\t") || strings.HasPrefix(line, "ok  \t") || strings.HasPrefix(line, "ok\t")
}

func firstField(s string) string {
	name, _, _ := strings.Cut(strings.TrimSpace(s), " ")
	return name
}
