package main

import (
	"io/fs"
	"path/filepath"
	"testing"
)

// ⛔ This file sorts FIRST on purpose. A test that panics ends the whole test
// binary, so the tests after it never run, and a guard held only by that panic
// is "caught" by luck (mutsweep: `caught by PANIC`). The tests here run before
// any that could panic, recover the panic themselves and report it with
// t.Errorf, so a deleted guard fails by assertion.

// noPanic runs f and turns a panic into a test failure naming the guard.
func noPanic(t *testing.T, guard string, f func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s: panicked (%v) -- the guard that refuses first is gone", guard, r)
		}
	}()
	f()
}

// main.go: the `err != nil` after newMutation. Without it the nil mutation is
// used by `defer m.restore()` and the process dies with a nil dereference.
func TestARefusedMutationIsReportedNotDereferenced(t *testing.T) {
	dir := t.TempDir()
	cases := map[string][]string{
		"the text is not in the file": {"-file", write(t, dir, "x.go", "package x\n"), "-from", "nope", "-to", "y", "--", "true"},
		"the file is not there":       {"-file", filepath.Join(dir, "missing.go"), "-from", "a", "-to", "b", "--", "true"},
	}
	for name, args := range cases {
		noPanic(t, name, func() {
			code, _, errb := runMutate(t, args...)
			if code != 2 {
				t.Errorf("%s: code=%d, want 2 (stderr %q)", name, code, errb)
			}
		})
	}
}

// main.go: `if !m.applied { return }` in restore. A restore of a mutation that
// was never applied (or already restored, as the deferred one is after the
// explicit one) must not write the file again.
func TestARestoreOfAnUnappliedMutationWritesNothing(t *testing.T) {
	path := write(t, t.TempDir(), "g.go", "keep me\n")
	m, err := newMutation(path, "keep", "drop", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	writes := 0
	m.write = func(string, []byte, fs.FileMode) error { writes++; return nil }
	noPanic(t, "restore before apply", m.restore)
	if writes != 0 {
		t.Errorf("restore of a mutation never applied wrote %d time(s), want 0", writes)
	}
	if err := m.apply(); err != nil {
		t.Fatal(err)
	}
	m.restore()
	m.restore()
	if writes != 2 {
		t.Errorf("apply, restore, restore wrote %d time(s), want 2 (the second restore must be a no-op)", writes)
	}
}
