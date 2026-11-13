package bench

import "testing"

var table = [4]int{1, 2, 3, 4}

// Fails by assertion (run with -bench .).
func BenchmarkA(b *testing.B) { b.Fatal("assertion: wrong value") }

// Fails only by a panic.
func BenchmarkP(b *testing.B) { n := len(table); _ = table[n] }
