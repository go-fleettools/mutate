package edges

import "testing"

func TestAsserted(t *testing.T) {
	if got := Asserted(9); got != "few" {
		t.Errorf("Asserted(9) = %q, want few", got)
	}
	if got := Asserted(10); got != "many" {
		t.Errorf("Asserted(10) = %q, want many", got)
	}
}

func TestIndexed(t *testing.T) {
	if got := Indexed(3); got != 40 {
		t.Errorf("Indexed(3) = %d, want 40", got)
	}
	// The edge is only walked, never compared: this is the test the guard
	// survives by panicking.
	if got := Indexed(4); got != -1 {
		t.Errorf("Indexed(4) = %d, want -1", got)
	}
}

func TestSpin(t *testing.T) { Spin(1) }

func TestUnheld(t *testing.T) {
	if got := Unheld(500); got != 100 {
		t.Errorf("Unheld(500) = %d, want 100", got)
	}
	if got := Unheld(5); got != 5 {
		t.Errorf("Unheld(5) = %d, want 5", got)
	}
}
