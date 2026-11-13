package fatalthenpanic

import "testing"

func TestA(t *testing.T) { t.Fatal("assertion: wrong value") }

func TestB(t *testing.T) {
	var a []int
	_ = a[3]
}
