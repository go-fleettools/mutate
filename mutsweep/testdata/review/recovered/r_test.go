package recovered

import "testing"

func TestR(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("panic: %v", r)
		}
	}()
	var a []int
	_ = a[3]
}
