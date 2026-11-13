package parallel

import (
	"testing"
	"time"
)

func TestA(t *testing.T) {
	t.Parallel()
	t.Error("assertion: wrong value")
	time.Sleep(200 * time.Millisecond)
}

func TestB(t *testing.T) {
	t.Parallel()
	time.Sleep(50 * time.Millisecond)
	var a []int
	_ = a[3]
}
