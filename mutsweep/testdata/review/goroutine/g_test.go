package goroutine

import "testing"

// The code under test panics in a goroutine it started: no test goroutine
// panics, the process dies. The test waits for it, so the panic always wins
// the race (with a WaitGroup whose Done is deferred, the test could finish
// first and the fixture would sometimes be green).
func work() {
	go func() {
		var a []int
		_ = a[3]
	}()
	select {}
}

func TestG(t *testing.T) { work() }
