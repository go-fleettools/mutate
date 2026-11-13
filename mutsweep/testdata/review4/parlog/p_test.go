package parlog

import (
	"testing"
	"time"
)

var table = [4]int{1, 2, 3, 4}

// Logs progress and is still running when B panics; it would have passed.
func TestA(t *testing.T) {
	t.Parallel()
	t.Log("warming the cache")
	time.Sleep(300 * time.Millisecond)
}

// Fails ONLY by a panic.
func TestB(t *testing.T) {
	t.Parallel()
	time.Sleep(50 * time.Millisecond)
	n := len(table)
	_ = table[n]
}
