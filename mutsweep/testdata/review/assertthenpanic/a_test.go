package assertthenpanic

import "testing"

func f() []int { return nil }

// The assertion fires, then the next line indexes past the result.
func TestA(t *testing.T) {
	got := f()
	if len(got) != 3 {
		t.Errorf("len = %d, want 3", len(got))
	}
	if got[2] != 7 {
		t.Errorf("got[2] = %d", got[2])
	}
}
