package a

import "testing"

// Fails by assertion, a leaf.
func TestParse(t *testing.T) { t.Error("assertion: wrong value") }
