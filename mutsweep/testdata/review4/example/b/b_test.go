package b

import "testing"

// Another package of the same run panics.
func TestP(t *testing.T) {
	var m map[string]int
	m["x"] = 1
}
