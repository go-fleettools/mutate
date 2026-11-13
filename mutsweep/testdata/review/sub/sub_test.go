package sub

import "testing"

// A panic inside a subtest only.
func TestX(t *testing.T) {
	t.Run("ok", func(t *testing.T) {})
	t.Run("boom", func(t *testing.T) {
		var a []int
		_ = a[3]
	})
}
