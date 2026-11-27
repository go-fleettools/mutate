//go:build !unix

package main

import "errors"

// availableBytes is unknown here: checkFreeSpace reads an error as "cannot tell",
// never as "full".
func availableBytes(string) (uint64, error) {
	return 0, errors.New("free space is not measured on this platform")
}
