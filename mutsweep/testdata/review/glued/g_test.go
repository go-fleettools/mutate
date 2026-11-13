package glued

import (
	"fmt"
	"testing"
)

// The code prints a progress mark without a newline, then panics.
func TestG(t *testing.T) {
	fmt.Print(".")
	var a []int
	_ = a[3]
}
