package main

import "testing"

// The edges of the panic window, asserted.
//
// A boundary sweep of this very file found `j < len(lines)` moved to `<=` held
// ONLY by an index-out-of-range panic: the tool's own verdict, on its own code.
// The loop is also exercised by the tests that run go for real, and the first
// panic ends the test binary, so whichever of them ran first would be the one
// to crash. This file's name sorts before the others so that THIS test, which
// recovers the panic and reports it as a failed assertion, runs first. That
// dependence on order is exactly the one the README documents for the verdict.
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
		got, crashed := func() (got bool, crashed any) {
			defer func() { crashed = recover() }()
			return hasPanic(tt.in), nil
		}()
		if crashed != nil {
			t.Errorf("%s: hasPanic panicked: %v", tt.name, crashed)
		} else if got != tt.want {
			t.Errorf("%s: hasPanic = %v, want %v", tt.name, got, tt.want)
		}
	}
}
