//go:build unix

package main

import "syscall"

// availableBytes is the space an unprivileged writer can still use under dir.
func availableBytes(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil //nolint:gosec // both are non-negative
}
