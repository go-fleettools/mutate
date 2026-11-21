package atomicwrite

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// posixOnly skips what only a POSIX filesystem can be asked.
//
// Windows has no permission bits — Go maps a file's mode to the read-only
// attribute — and a read-only DIRECTORY there does not stop a file being
// created inside it. Both are needed to arrange a real failure, so those tests
// are POSIX-only.
//
// ⚠ This is part of why the per-step failures below are INJECTED rather than
// arranged: an injected error is the same error on every platform, so the five
// guards it holds are held on Windows too.
func posixOnly(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs POSIX directory permissions and mode bits")
	}
}

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
	posixOnly(t)
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

// ⛔ A FILE THAT IS THERE KEEPS THE MODE IT HAD, whatever the caller passes.
// CreateTemp makes its file 0o600 and the rename installs that new inode, so
// without the chmod every file a sweep touched would come back private — and a
// caller passing 0o644, as all of them used to, would WIDEN a 0o600 file. The
// second argument is for a file that does not exist; the mode of one that does
// is the file's own business.
func TestAFileThatIsThereKeepsTheModeItHad(t *testing.T) {
	posixOnly(t)
	for _, had := range []os.FileMode{0o600, 0o644, 0o755} {
		dir := t.TempDir()
		path := filepath.Join(dir, "g.go")
		if err := os.WriteFile(path, []byte("x\n"), had); err != nil {
			t.Fatal(err)
		}
		// Asked for something else entirely, on purpose.
		if err := Write(path, []byte("y\n"), 0o666); err != nil {
			t.Fatalf("Write: %v", err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != had {
			t.Errorf("mode = %o, want the %o it had", got, had)
		}
	}
}

// And a file that is NOT there gets the mode asked for, which is the only case
// the argument governs.
func TestAFileThatIsNotThereGetsTheModeAskedFor(t *testing.T) {
	posixOnly(t)
	for _, perm := range []os.FileMode{0o600, 0o644, 0o755} {
		path := filepath.Join(t.TempDir(), "new.go")
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
	posixOnly(t)
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

// ⛔ THE STAND-IN IS ASSERTED BY ITS TYPE, NOT BY CALLING IT, and the order of
// these two checks is the point. A nil *os.File inside a non-nil interface is
// not nil to the language, so `tmp == nil` would be false and `tmp.Name()`
// would CRASH — the very thing the stand-in exists to prevent would be
// reported as a panic again. Asking what it IS fails by assertion instead.
func TestTheWrapperNeverHandsBackTheNilFileItself(t *testing.T) {
	posixOnly(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	tmp, err := createTemp(dir, "x-*")
	if err == nil {
		t.Fatal("making a temporary file in an unwritable directory reported success")
	}
	if _, isFile := tmp.(*os.File); isFile {
		t.Fatal("it handed back the *os.File itself, which is nil here: Name() crashes instead of answering")
	}
	if got := tmp.Name(); got != "" {
		t.Errorf("Name() = %q, want the empty string", got)
	}
}

// fakeTemp is a real temporary file that fails at ONE named step, so each
// guard can be asked about on its own.
type fakeTemp struct {
	file *os.File
	fail string
	err  error
}

func (f *fakeTemp) Write(p []byte) (int, error) {
	if f.fail == "write" {
		return 0, f.err
	}
	return f.file.Write(p)
}

func (f *fakeTemp) Sync() error {
	if f.fail == "sync" {
		return f.err
	}
	return f.file.Sync()
}

// ⛔⛔ THE REAL FILE IS ALWAYS CLOSED, EVEN WHEN AN ERROR IS REPORTED, and
// getting that wrong manufactured a defect on a platform this machine cannot
// run. (*os.File).Close releases the descriptor and THEN returns its error; a
// fake that returned an error while keeping the handle open does not model it.
// On Windows an open file cannot be deleted, so the deferred os.Remove failed
// and left the temporary behind — "the directory holds 2 entries, want 1" on
// windows-latest, 2026-11-21, green on macOS and Linux. The writer was right
// and the stand-in was lying about what a failure looks like.
func (f *fakeTemp) Close() error {
	err := f.file.Close()
	if f.fail == "close" {
		return f.err
	}
	return err
}

func (f *fakeTemp) Name() string { return f.file.Name() }

// ⛔⛔ EVERY ONE OF THESE GUARDS WAS A SURVIVOR, and the first was worse than
// one: mutsweep went red on it by PANICKING on a nil file, which is luck and
// not a test — the next edit that gives the nil a name turns it into a silent
// survivor too.
//
// The point of each guard is the same, and it is the point of the package: a
// step that cannot complete must leave the target holding ALL of its old
// bytes. The mutation these hold is the guard being skipped, after which Write
// runs on to the next step and ends up reporting success — so every case
// asserts that an error came back, that it names the file and carries the
// cause, and that the original is still there.
//
// ⛔ THE CREATE CASE RETURNS A USABLE FILE BESIDE THE ERROR, on purpose. That
// is what moves this from a panic to an assertion: with the guard mutated
// away, Write carries on with a working file and returns nil, and the "no
// error" line below is what fails. A nil file would merely crash, which proves
// nothing about what the guard is for.
func TestEachStepThatCannotCompleteLeavesTheOriginalWhole(t *testing.T) {
	const original = "package g\n\n// 434 lines of somebody's afternoon\n"
	boom := errors.New("the disk said no")

	for _, tc := range []struct {
		name, fail string
		// On the create case the stray temporary is the FAKE's own — Write
		// never learned its name. The real refusal leaves nothing behind, and
		// TestARefusedWriteLeavesNothingBehind measures that on a real
		// filesystem.
		wantNoLeftover bool
	}{
		{"the temporary file cannot be created", "create", false},
		{"the bytes cannot be written", "write", true},
		{"the bytes cannot be flushed to the disk", "sync", true},
		{"the temporary file cannot be closed", "close", true},
		{"the mode cannot be set", "chmod", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "g.go")
			if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
				t.Fatal(err)
			}

			realCreate, realChmod := createTemp, chmod
			t.Cleanup(func() { createTemp, chmod = realCreate, realChmod })
			createTemp = func(d, p string) (tempFile, error) {
				f, err := os.CreateTemp(d, p)
				if err != nil {
					t.Fatalf("the test's own temporary file: %v", err)
				}
				t.Cleanup(func() { _ = f.Close() })
				tf := &fakeTemp{file: f, fail: tc.fail, err: boom}
				if tc.fail == "create" {
					return tf, boom
				}
				return tf, nil
			}
			if tc.fail == "chmod" {
				chmod = func(string, fs.FileMode) error { return boom }
			}

			err := Write(path, []byte("mutated\n"), 0o600)
			if err == nil {
				t.Fatalf("a write whose %s step failed reported success", tc.fail)
			}
			if !errors.Is(err, boom) {
				t.Errorf("error = %v, which does not carry the cause", err)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error = %v, which does not name the file", err)
			}

			got, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("the file is gone: %v", readErr)
			}
			if string(got) != original {
				t.Errorf("contents = %q, want the original back", got)
			}
			if tc.wantNoLeftover {
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 1 {
					t.Errorf("the directory holds %d entries, want 1", len(entries))
				}
			}
		})
	}
}

// The rename is the one step a real filesystem refuses on demand: a directory
// cannot be replaced by a file. No seam, therefore — the guard is held by the
// kernel, which is a better witness than an injected error.
func TestARenameThatCannotHappenIsReported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "in-the-way")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "keep"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := Write(path, []byte("mutated\n"), 0o644)
	if err == nil {
		t.Fatal("a rename onto a non-empty directory reported success")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error = %v, which does not name the target", err)
	}
	fi, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("the directory is gone: %v", statErr)
	}
	if !fi.IsDir() {
		t.Fatal("the directory was replaced by the write")
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 1 {
		t.Errorf("the directory holds %d entries, want 1 — the temporary was left behind", len(entries))
	}
}
