package atomicwrite

import (
	"os"
	"path/filepath"
	"testing"
)

func TestItReplacesTheContents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "g.go")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("after\n"), 0o644); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "after\n" {
		t.Errorf("contents = %q", got)
	}
}

// It must also create a file that does not exist yet, since that is what a
// plain WriteFile did.
func TestItCreatesAFileThatIsNotThereYet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.go")
	if err := Write(path, []byte("fresh\n"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "fresh\n" {
		t.Errorf("contents = %q", got)
	}
}

// ⛔⛔ THE DISCRIMINATOR, and the whole reason this package exists.
//
// ⚠ IT DOES NOT REPRODUCE A SIGKILL. Killing a process inside the truncate
// window is not something a test can schedule. What it pins is the property
// that makes a kill survivable: the target is NEVER OPENED FOR TRUNCATION, so
// a write that cannot complete leaves the old bytes untouched.
//
// An unwritable DIRECTORY is the one failure a test can arrange that separates
// the two implementations. os.WriteFile does not need to create anything — it
// opens the existing file with O_TRUNC, which the directory's permissions do
// not govern — so it succeeds in emptying the file. CreateTemp needs to make a
// new entry, fails, and nothing is touched.
//
// Swap Write's body for os.WriteFile and this test goes red with "the file was
// emptied by a write that could not complete".
func TestTheOriginalSurvivesAWriteThatCannotComplete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "g.go")
	const original = "package g\n\n// 434 lines of somebody's afternoon\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	// r-x: existing files stay writable, new entries cannot be made.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	// Put it back, or t.TempDir cannot clean up after itself.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := Write(path, []byte("mutated\n"), 0o644)
	if err == nil {
		t.Fatal("a write into an unwritable directory reported success")
	}

	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("the file is gone: %v", readErr)
	}
	if len(got) == 0 {
		t.Fatal("the file was emptied by a write that could not complete — this is the 2026-11-21 defect")
	}
	if string(got) != original {
		t.Errorf("contents changed to %q", got)
	}
}

// The mode asked for is the mode the file ends up with. CreateTemp makes its
// file 0o600, so without the chmod every restored file would come back
// private — and a restore that hardcoded 0o644 used to widen a 0o600 file
// instead.
func TestThePermissionsAreTheOnesAskedFor(t *testing.T) {
	for _, perm := range []os.FileMode{0o600, 0o644, 0o755} {
		dir := t.TempDir()
		path := filepath.Join(dir, "g.go")
		if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := Write(path, []byte("y\n"), perm); err != nil {
			t.Fatalf("Write: %v", err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != perm {
			t.Errorf("mode = %o, want %o", got, perm)
		}
	}
}

// A sweep makes thousands of writes. One leftover temporary per write would
// fill the directory it is sweeping, and the next `go build ./...` would try
// to compile them.
func TestNoTemporaryFileIsLeftBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "g.go")
	if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if err := Write(path, []byte("y\n"), 0o644); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the directory holds %d entries, want 1: %v", len(entries), names)
	}
}

// And a refused write leaves none either: the directory it would not touch
// must not end up holding a stray temporary from the attempt.
func TestARefusedWriteLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "g.go")
	if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(sub, "h.go")
	if err := os.WriteFile(inner, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sub, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o700) })

	if err := Write(inner, []byte("y\n"), 0o644); err == nil {
		t.Fatal("a write into an unwritable directory reported success")
	}
	entries, err := os.ReadDir(sub)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the directory holds %d entries, want 1", len(entries))
	}
}
