// Package edges holds four range guards, each held differently, so that a
// boundary sweep has to give each its own verdict. The tests live beside it and
// are the point: mutsweep copies this package and mutates the copy.
package edges

var table = [4]int{10, 20, 30, 40}

// Asserted is held by an assertion: moving the edge changes a value a test
// compares.
func Asserted(n int) string {
	if n > 9 {
		return "many"
	}
	return "few"
}

// Indexed is held only by luck: past the table the next line indexes out of
// range, and no test asserts what the guard returns for the value at the edge
// except by walking into the panic.
func Indexed(x int) int {
	if x >= 4 {
		return -1
	}
	return table[x]
}

// Spin is held only by a timeout: without the guard it never returns.
func Spin(n int) {
	if n >= 1 {
		return
	}
	select {}
}

// Unheld is held by nothing: at the edge both sides give the same value, so the
// moved comparison is the same function.
func Unheld(x int) int {
	if x > 100 {
		return 100
	}
	return x
}
