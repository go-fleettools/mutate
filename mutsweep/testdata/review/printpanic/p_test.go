package printpanic

import (
	"fmt"
	"testing"
)

func TestA(t *testing.T) { t.Fatal("assertion: wrong value") }

// A PASSING test whose code prints a line beginning "panic:" on stdout
// (a subprocess's stderr copied through, a recover handler's report...).
func TestB(t *testing.T) { fmt.Println("panic: handled by the retry loop") }
