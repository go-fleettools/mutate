package b

import "testing"

// Same top-level name as package a, panics in a subtest.
func TestParse(t *testing.T) {
	t.Run("edge", func(t *testing.T) {
		var a []int
		_ = a[3]
	})
}
