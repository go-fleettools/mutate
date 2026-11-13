package stackdump

import (
	"fmt"
	"runtime"
	"testing"
)

// Passes: reports a recovered crash the way a crash handler does, with stacks.
func TestDump(t *testing.T) {
	fmt.Println("panic: recovered by the worker pool")
	buf := make([]byte, 1<<16)
	fmt.Print(string(buf[:runtime.Stack(buf, false)]))
}

// Fails with no message at all (t.FailNow, or os.Exit(1) in code under test).
func TestFail(t *testing.T) { t.FailNow() }
