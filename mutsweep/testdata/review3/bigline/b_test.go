package bigline

import (
	"strings"
	"testing"
)

// Panics with one 100 KB line: test2json splits long lines into several events.
func TestB(t *testing.T) {
	panic(strings.Repeat("x", 100_000))
}
