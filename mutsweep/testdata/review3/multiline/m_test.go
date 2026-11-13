package multiline

import (
	"strings"
	"testing"
)

// Panics with an error whose message spans 20 lines (a joined error, a
// formatted table): the goroutine header comes after them.
func TestM(t *testing.T) {
	panic(strings.Repeat("line of a joined error\n", 20))
}
