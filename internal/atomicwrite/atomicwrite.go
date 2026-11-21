// Package atomicwrite replaces a file's contents in one filesystem step.
//
// ⛔⛔ IT EXISTS BECAUSE A KILLED SWEEP EMPTIED A SOURCE FILE.
//
// Measured 2026-11-21: a mutsweep run was killed (exit 137 — SIGKILL) while
// sweeping go-fleettools/prwait, and left quota.go at ZERO BYTES. Not mutated,
// not half-written: 434 lines gone, and `wc -l` answered 0. The file happened
// to be committed, so nothing was lost; had it been an hour of uncommitted
// work, the tool whose whole purpose is to put a file back would have been the
// thing that destroyed it.
//
// The cause is that os.WriteFile TRUNCATES AND THEN WRITES. Between those two
// steps the file is empty on disk, and a process that dies in that window —
// SIGKILL, an OOM kill, a panic in another goroutine, the machine losing power
// — leaves it empty. mutate already restores on a signal (see the handler in
// cmd mutate), but SIGKILL CANNOT BE TRAPPED, so no handler can cover this.
// The window has to stop existing instead.
//
// ⚠ The pattern was already in this repository, in mutsweep/record.go, which
// writes its RECORD with CreateTemp + Rename. The ledger was written
// atomically and the user's source code was not.
package atomicwrite

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// tempFile is the part of *os.File that Write uses.
//
// ⛔ IT IS AN INTERFACE, AND createTemp AND chmod ARE VARIABLES, FOR ONE
// REASON: so that a test can make each step fail. No arrangement of a real
// filesystem that a test is allowed to build makes tmp.Write, tmp.Sync,
// tmp.Close, or a chmod of a file this very process just created, return an
// error — and mutsweep answered SURVIVED for all four of those guards plus the
// CreateTemp one, which only went red by PANICKING on a nil file. A guard no
// test can reach is a guard that is not there: the next edit removes it and
// nothing says so.
//
// ⚠ The RENAME is deliberately NOT behind a seam. Renaming onto an existing
// directory fails for real, so that guard is held by the filesystem itself,
// and a real failure is worth more than an injected one.
type tempFile interface {
	Write(p []byte) (int, error)
	Sync() error
	Close() error
	Name() string
}

// unusable stands in for a temporary file that could not be created.
//
// ⛔ A NIL FILE IS NOT AN ANSWER, and handing one back turned a missing guard
// into a crash. os.CreateTemp returns a nil *os.File beside its error, and
// (*os.File).Name DEREFERENCES its receiver — Write, Sync and Close all answer
// ErrInvalid on a nil file, Name alone does not. So with the create guard
// deleted this package CRASHED instead of answering wrongly, and mutsweep is
// right that a crash is luck: it goes red because the next line happens to
// dereference, and the first edit that moves that line leaves nothing holding
// the guard at all. With a stand-in, the guard's absence is a WRONG ANSWER —
// the create error replaced by a later one — which a test can assert about.
type unusable struct{ err error }

func (u unusable) Write([]byte) (int, error) { return 0, u.err }
func (u unusable) Sync() error               { return u.err }
func (u unusable) Close() error              { return u.err }
func (u unusable) Name() string              { return "" }

var (
	createTemp = func(dir, pattern string) (tempFile, error) {
		f, err := os.CreateTemp(dir, pattern)
		// On nil, not on the error: the nil is what would be dereferenced.
		if f == nil {
			return unusable{err: err}, err
		}
		return f, err
	}
	chmod = os.Chmod
)

// Write replaces path's contents with data, keeping the mode path already has.
// permIfNew is used only when path does not exist yet.
//
// The new bytes are written to a temporary file in path's OWN directory and
// then renamed over it. A rename within one filesystem is atomic, so at every
// instant path holds either all of its old contents or all of the new ones:
// there is no moment at which it is empty or short. A reader always sees one
// or the other, and a kill at any point leaves one or the other.
//
// ⛔ THE TEMPORARY FILE MUST SHARE THE DIRECTORY. Renaming across filesystems
// is not atomic — it is a copy and an unlink — so a temporary in the system
// temp directory would reintroduce exactly the window this removes, while
// looking like it had closed it.
func Write(path string, data []byte, permIfNew fs.FileMode) error {
	// ⛔ THE MODE IS A PROPERTY OF THE FILE, AND IT IS READ HERE RATHER THAN
	// ASKED OF THE CALLER. Every caller wanted the same thing — put the file
	// back exactly as it was — and each one had to stat the file to say so,
	// which gave every call site an error path that CANNOT BE REACHED: a stat
	// that fails after the bytes were read is a race no test can arrange.
	// mutsweep answered SURVIVED for two of them, and an unreachable guard is
	// not a guard, it is a line that will never be read again. Asked once, here,
	// where the rename makes it necessary.
	perm := permIfNew
	if fi, err := os.Stat(path); err == nil {
		perm = fi.Mode().Perm()
	}
	dir := filepath.Dir(path)
	tmp, err := createTemp(dir, "."+filepath.Base(path)+".atomicwrite-*")
	if err != nil {
		// ⛔ RETURNED, NOT WORKED AROUND. An unwritable directory means this
		// write cannot be made safely, and falling back to a truncating
		// write would be choosing the data loss this package exists to
		// prevent. The caller is told; path keeps what it had.
		return fmt.Errorf("atomicwrite %s: %w", path, err)
	}
	name := tmp.Name()
	// After a successful rename this removes nothing (the name is gone); on
	// every failure path it takes the temporary file with it, so a refused
	// write does not litter the directory it refused to touch.
	defer os.Remove(name)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("atomicwrite %s: %w", path, err)
	}
	// ⚠ Sync before the rename, because a rename is atomic with respect to
	// the NAME and says nothing about the DATA having reached the disk. Without
	// it, a machine that loses power just after the rename can bring the name
	// back pointing at a file whose blocks were never written — the empty file
	// again, by a longer road. It costs one fsync per mutation, which is not
	// measurable beside the `go test` run each mutation exists to drive.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("atomicwrite %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		// ⚠ The target is untouched either way. What a failing Close can leave
		// behind is the TEMPORARY: on Windows an open file cannot be deleted,
		// so a Close that fails without releasing the handle would defeat the
		// deferred removal. A stray dotfile beside the target is the whole
		// cost, and it is not worth a second Close to chase.
		return fmt.Errorf("atomicwrite %s: %w", path, err)
	}
	// ⛔ THE MODE HAS TO BE CARRIED DELIBERATELY NOW, and it did not before.
	// os.WriteFile passes perm to OpenFile, which APPLIES IT ONLY WHEN
	// CREATING the file — so writing over an existing file left its mode
	// alone, and the old call sites' hardcoded 0o644 was inert. A rename
	// installs a NEW inode, which carries the temporary file's own mode:
	// CreateTemp makes that 0o600, so without this chmod every file a sweep
	// touched would come back private. Set before the rename, so path is
	// never visible with the wrong permissions.
	if err := chmod(name, perm); err != nil {
		return fmt.Errorf("atomicwrite %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("atomicwrite %s: %w", path, err)
	}
	return nil
}
