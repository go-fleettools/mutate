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
// ⛔ AMBIGUITY RESOLVES TO PANIC. A false alarm costs one look at a report; a
// false certificate is the strongest false reassurance this tool can give. Every
// rule below is written so that when the output cannot say, the answer is
// `caught by PANIC`.
//
// The rule: a red suite is CAUGHT BY PANIC when the output holds a panic, and
// CAUGHT when it holds none, or when some test that did not panic printed an
// assertion line. The lines a test printed are NOT assertions when that test
// panicked, or when it is an ancestor of one that did, or -- when the panic cannot
// be attributed to a test at all -- when they belong to the package it killed.
//
// Text cannot tell t.Log from t.Error: both print `file_test.go:7: ...`. So
// `t.Logf("x = %d", x)` followed by an index panic would read as an assertion
// if the lines of the test that panicked counted, and every test that logs
// progress would be certified. They do not count.
//
// The first versions were wrong in both directions on real go1.27.4 output, and
// the fixtures are now the real thing (testdata/review, testdata/review3):
//
//   - test2json files a subtest's panic under the PARENT test, so a rule about
//     "leaf tests" dropped the only test that panicked;
//   - a panic with no test-level fail event (a `go test -timeout` expiry, a panic
//     in a goroutine the code started) named no failing test at all;
//   - a fixed window for the goroutine trace missed a panic message of twenty
//     lines, and a panic line of 100 KB that test2json delivers in fragments;
//   - a panic value that prints like an assertion line was read as one;
//   - a stack dump printed by a test that PASSED was read as a panic.
//
// ⛔ THE VERDICT DEPENDS ON WHICH TESTS RAN. The first panic ends its test
// binary, so a test that would have asserted afterwards never runs, and the
// verdict can differ with test order (-shuffle, -run, a different package order).
// A panic red is reported as luck because the assertion it needs may simply not
// have been reached; run the failing test alone to ask. Plain output without
// -v is the weakest reading: it prints a test's lines only when the test
// finishes, so the assertion of a parallel test still running when another
// panics is lost, and the report says so.

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

// decide is the rule itself, kept apart from both readers. `passed` holds the
// verdict of each test that reported one: true for a pass or a skip, false for a
// fail, absent for a test that never reported (still running when a panic ended
// the binary).
//
// ⛔ An assertion line counts only from a test that FAILED, by a fail event or a
// `--- FAIL` line of its own. A test that is merely not known to have passed
// may be one that was still running, logging progress, and would have passed:
// its t.Log is not an assertion.
func decide(lines []outLine, passed map[testKey]bool) verdict {
	tainted, killed, found := panicked(lines, passed)
	if !found {
		return caught
	}
	for _, l := range lines {
		k := testKey{l.pkg, l.test}
		if ok, reported := passed[k]; !reported || ok || tainted[k] || killed[l.pkg] {
			continue
		}
		if assertionLine.MatchString(l.text) {
			return caught
		}
	}
	return caughtByPanic
}

// panicked finds the panics in the output, from tests not known to have passed.
//
// A panic is a line holding `panic:` followed, at any distance, by the runtime's
// `goroutine N [state]:` header. The header is the evidence; a fixed distance to
// it is not, because a panic message can run to any number of lines. A passing
// test that prints the word, or a stack dump, is not a panic of the suite: it did
// not fail by it.
//
// It returns the tests whose lines are not assertions (the test the panic is
// filed under and all its ancestors, since test2json files a subtest's panic
// under its parent), the packages in which a panic could not be filed under any
// test, and whether there was a panic at all.
func panicked(lines []outLine, passed map[testKey]bool) (tainted map[testKey]bool, killed map[string]bool, found bool) {
	tainted, killed = map[testKey]bool{}, map[string]bool{}
	for i, l := range lines {
		if !strings.Contains(l.text, "panic:") || passed[testKey{l.pkg, l.test}] {
			continue
		}
		if !traceFollows(lines[i+1:]) {
			continue
		}
		found = true
		if l.test == "" {
			killed[l.pkg] = true
			continue
		}
		// The test and each ancestor: TestX/sub/leaf taints TestX/sub and TestX.
		for name := l.test; name != ""; {
			tainted[testKey{l.pkg, name}] = true
			cut := strings.LastIndexByte(name, '/')
			if cut < 0 {
				break
			}
			name = name[:cut]
		}
	}
	return tainted, killed, found
}

// traceFollows is whether a `goroutine N [state]:` header comes after.
func traceFollows(rest []outLine) bool {
	for _, l := range rest {
		if strings.HasPrefix(l.text, "goroutine ") && strings.Contains(l.text, " [") &&
			strings.HasSuffix(strings.TrimSpace(l.text), "]:") {
			return true
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

// readEvents reads `go test -json` output. test2json splits a long line into
// several output events, so fragments are joined per test until the newline: a
// 100 KB panic line is one line, not forty.
func readEvents(out []byte) (lines []outLine, passed map[testKey]bool, ok bool) {
	passed = map[testKey]bool{}
	pending := map[testKey]string{}
	var order []testKey
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
		k := testKey{e.Package, e.Test}
		switch e.Action {
		case "output":
			if _, open := pending[k]; !open {
				order = append(order, k)
			}
			text := pending[k] + e.Output
			for {
				nl := strings.IndexByte(text, '\n')
				if nl < 0 {
					break
				}
				lines = append(lines, outLine{k.pkg, k.test, text[:nl]})
				text = text[nl+1:]
			}
			pending[k] = text
		case "pass", "skip":
			if e.Test != "" {
				passed[k] = true
			}
		case "fail":
			if e.Test != "" {
				passed[k] = false
			}
		}
	}
	// A last line with no newline is still a line.
	for _, k := range order {
		if rest := pending[k]; rest != "" {
			lines = append(lines, outLine{k.pkg, k.test, rest})
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
			passed[testKey{pkg, test}] = false
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
