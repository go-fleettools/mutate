package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"path/filepath"
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
// So a red suite is read once more. It is caught by PANIC when it failed, and
// every test that failed did so through a panic and none through an assertion.

// how says which way a red suite was read, because the two are not equally
// exact and the report has to say which one it used.
type how string

const (
	// byJSON reads the `go test -json` events, where each line of output is
	// already attributed to the test that printed it.
	byJSON how = "go test -json events"
	// byText reads the plain output, attributing a line to the last test
	// whose `--- FAIL:` line preceded it. That is how the go tool prints a panic
	// (the FAIL line, then the unindented `panic:` and its trace), but it is a
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

// failure is one test that failed, with whether it panicked.
type failure struct {
	pkg, name string
	panicked  bool
}

// isPanicLine is the line the Go runtime prints when a goroutine dies of a
// panic. It must BEGIN the line: a test that logs the word, or an assertion
// that quotes one in its message, is indented or prefixed by a file position.
func isPanicLine(line string) bool { return strings.HasPrefix(line, "panic:") }

// classify reads what a red suite said and returns caught or caughtByPanic.
//
// Nothing found to attribute is CAUGHT, never panic: the claim that a guard is
// held only by luck needs evidence, and a red suite that names no failing test
// (a TestMain that exits 1, a vet failure) offers none.
func classify(out []byte, json bool) (verdict, how) {
	var fails []failure
	var orphanPanic bool
	method := byText
	if json {
		method = byJSON
		var ok bool
		if fails, orphanPanic, ok = readEvents(out); !ok {
			// No event at all: whatever ran did not speak JSON, so say that the
			// plain reading is what answered.
			method = byText
			fails, orphanPanic = readText(out)
		}
	} else {
		fails, orphanPanic = readText(out)
	}
	return decide(fails, orphanPanic), method
}

// decide is the rule itself, kept apart from both readers.
func decide(fails []failure, orphanPanic bool) verdict {
	// A parent fails because a subtest did; what it says is that subtest's
	// verdict, so only the LEAVES are counted.
	var leaves, panicked int
	for _, f := range fails {
		if hasFailingChild(fails, f) {
			continue
		}
		leaves++
		if f.panicked {
			panicked++
		}
	}
	switch {
	case leaves == 0:
		// A panic outside any test's own goroutine ends the process without
		// ever printing a failing test.
		if orphanPanic {
			return caughtByPanic
		}
		return caught
	case panicked == leaves:
		return caughtByPanic
	default:
		return caught
	}
}

func hasFailingChild(fails []failure, parent failure) bool {
	for _, f := range fails {
		if f.pkg == parent.pkg && strings.HasPrefix(f.name, parent.name+"/") {
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

func readEvents(out []byte) (fails []failure, orphanPanic, ok bool) {
	type key struct{ pkg, test string }
	panicked := map[key]bool{}
	var failed []key
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var e event
		if err := json.Unmarshal(line, &e); err != nil || e.Action == "" {
			continue
		}
		ok = true
		k := key{e.Package, e.Test}
		switch e.Action {
		case "output":
			if isPanicLine(e.Output) {
				if e.Test == "" {
					orphanPanic = true
				} else {
					panicked[k] = true
				}
			}
		case "fail":
			if e.Test != "" {
				failed = append(failed, k)
			}
		}
	}
	for _, k := range failed {
		fails = append(fails, failure{pkg: k.pkg, name: k.test, panicked: panicked[k]})
	}
	return fails, orphanPanic, ok
}

// readText attributes each line to the test whose `--- FAIL:` line came last.
//
// The go tool prints a panicking test as
//
//	--- FAIL: TestX (0.00s)
//	panic: runtime error: index out of range [3] with length 3 ...
//
// so a panic line belongs to the failing test above it, until the next result
// line or the package's own FAIL line ends the stretch.
func readText(out []byte) (fails []failure, orphanPanic bool) {
	cur := -1
	for _, line := range strings.Split(string(out), "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		switch {
		case strings.HasPrefix(trimmed, "--- FAIL: "):
			name := strings.Fields(strings.TrimPrefix(trimmed, "--- FAIL: "))
			if len(name) == 0 {
				cur = -1
				continue
			}
			fails = append(fails, failure{name: name[0]})
			cur = len(fails) - 1
			continue
		case strings.HasPrefix(trimmed, "--- "), strings.HasPrefix(trimmed, "=== "),
			line == "FAIL", strings.HasPrefix(line, "FAIL\t"), strings.HasPrefix(line, "FAIL "),
			strings.HasPrefix(line, "ok\t"), strings.HasPrefix(line, "ok "):
			cur = -1
		}
		if isPanicLine(line) {
			if cur >= 0 {
				fails[cur].panicked = true
			} else {
				orphanPanic = true
			}
		}
	}
	return fails, orphanPanic
}
