// Package saidpanic is the fixture for go-fleettools/mutate#28: a test that
// fails by ASSERTION, whose message happens to contain the word `panic:`,
// followed by another test that panics for real.
//
// The first test is held by an assertion and nothing else; the word is in the
// message because the code under test recovers a crash and reports it as an
// error, which is exactly what a guard's decline test looks like. The guard it
// holds was reported `caught by PANIC` — a verdict that says "this red is luck"
// about a red that is an assertion.
package saidpanic

import (
	"fmt"
	"testing"
)

// verify is the shape the real case had (go-pdfkit/reader's signature.Verify):
// it recovers and wraps, so its caller gets an error rather than a crash.
func verify() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	var p *int
	_ = *p
	return nil
}

// Fails by assertion. Its message carries the word, at an INDENTED source
// position, which is the whole distinction: the runtime prints a panic at
// column 0.
func TestVerifyRefusesAnUnreadableInput(t *testing.T) {
	if err := verify(); err != nil {
		t.Errorf("empty: %v, want an error wrapping ErrMalformed", err)
	}
}

// Then something crashes for real, which is what makes the first test's
// message ambiguous to a reader that looks for a trace "at any distance".
func TestSomethingElseCrashes(t *testing.T) {
	table := [4]int{1, 2, 3, 4}
	n := len(table)
	_ = table[n]
}
