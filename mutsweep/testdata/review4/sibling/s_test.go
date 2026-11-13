package sibling

import "testing"

var table = [4]int{1, 2, 3, 4}

// One subtest asserts, its sibling panics.
func TestS(t *testing.T) {
	t.Run("asserts", func(t *testing.T) { t.Error("assertion: wrong value") })
	t.Run("panics", func(t *testing.T) { n := len(table); _ = table[n] })
}
