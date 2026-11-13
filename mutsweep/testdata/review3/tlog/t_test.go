package tlog

import "testing"

var table = [4]int{1, 2, 3, 4}

// Fails ONLY by a panic; it logs progress first, as many tests do.
func TestT(t *testing.T) {
	for x := 0; x <= 4; x++ {
		t.Logf("x = %d", x)
		_ = table[x]
	}
}
