package panicfirst

import "testing"

// A panics first and kills the binary; B, which asserts, never runs.
func TestA(t *testing.T) {
	var a []int
	_ = a[3]
}

func TestB(t *testing.T) { t.Error("assertion: wrong value") }
