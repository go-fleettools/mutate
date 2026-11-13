package nilmap

import "testing"

// A subtest fails by assertion, then the parent panics after it.
func TestP(t *testing.T) {
	t.Run("asserts", func(t *testing.T) { t.Error("assertion: wrong value") })
	var m map[string]int
	m["x"] = 1
}
