package framepanic

import (
	"fmt"
	"testing"
)

type E struct{}

func (E) Error() string { return "\n    decode_test.go:12: bad header" }

// Fails only by a panic whose value prints like an assertion line.
func TestF(t *testing.T) { panic(fmt.Errorf("wrap:%w", E{})) }
