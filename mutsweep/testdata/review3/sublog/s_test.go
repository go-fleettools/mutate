package sublog

import "testing"

var table = [4]int{1, 2, 3, 4}

// Table-driven: the parent logs each case, the subtest only panics.
func TestS(t *testing.T) {
	for _, x := range []int{0, 4} {
		t.Logf("case %d", x)
		t.Run("", func(t *testing.T) { _ = table[x] })
	}
}
