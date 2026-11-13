package helper

import "testing"

func check(t *testing.T, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("got %d, want %d", got, want)
	}
}

// Fails by t.Fatal inside a t.Helper; a later test panics.
func TestH(t *testing.T) { check(t, 1, 2) }

func TestP(t *testing.T) {
	var m map[string]int
	m["x"] = 1
}
