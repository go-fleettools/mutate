package main

// ⛔ THESE TESTS EXIST BECAUSE THE GATE FAILED ITS OWN STANDARD.
//
// `mutsweep -dir mutsweep -files hook.go,record.go` over hook_test.go alone, on
// the first version of this gate:
//
//	27 mutations: 2 caught, 0 caught by PANIC, 8 not mutants, 17 survived
//
// Seventeen of its own refusals were held by nothing -- a push of hook.go would
// have been refused by hook.go. Each survivor was read before a test was
// written, as the report asks. What is below holds the ones that are a
// behaviour; the ones that are not are named in the PR, not silently left:
//
//	hook.go:336   len(pushes) == 0  a fast path: the general path over zero
//	                                pushes answers the same silent 0
//	hook.go:204   diff fails after merge-base succeeded   } unreachable without
//	hook.go:229   HEAD lacks a file diff just listed      } faking git or json,
//	record.go:126 json.Marshal of a plain struct          } and a fake that
//	record.go:176 a swept file vanishes before filing     } proves only itself

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeRaw puts bytes where the record for key would be.
func writeRaw(t *testing.T, dir, key, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, key+".json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func committedKey(t *testing.T, work, file string) string {
	t.Helper()
	return contentKey([]byte(gitIn(t, work, "show", "HEAD:"+file)))
}

// hook.go:247. A file with NO refusal has nothing to delete and nothing to
// cover: without that line it would look up a record no sweep can ever write
// -- a sweep of it is refused as "no refusal found" -- and be refused forever.
func TestAGoFileWithNoRefusalIsNotJudged(t *testing.T) {
	t.Setenv(recordsEnv, t.TempDir())
	work := newPushRepo(t)
	gitIn(t, work, "checkout", "main")
	gitIn(t, work, "checkout", "-b", "plain")
	if err := os.WriteFile(filepath.Join(work, "h.go"), []byte("package g\n\n// Double has no refusal.\nfunc Double(n int) int { return 2 * n }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "add", "h.go")
	gitIn(t, work, "commit", "-m", "a file with no refusal")
	if code, msg, why := hookOn(t, work, "gitpush"); code != 0 || msg != "" || why != "" {
		t.Errorf("a file with nothing to sweep was judged: exit %d, %q, %q", code, msg, why)
	}
}

// ⛔ record.go:109. A record names the bytes it was swept at; one filed under
// another key has been moved or edited, and trusting it would let a record
// vouch for content it never saw. Refused, not allowed.
func TestARecordFiledUnderAnotherKeyIsNotTrusted(t *testing.T) {
	store := t.TempDir()
	t.Setenv(recordsEnv, store)
	work := newPushRepo(t)
	key := committedKey(t, work, "g.go")
	forged, _ := json.Marshal(record{SHA256: strings.Repeat("0", 64), File: "g.go", Guards: 2})
	writeRaw(t, store, key, string(forged))
	code, _, why := hookOn(t, work, "gitpush")
	if code != 2 || !strings.Contains(why, "cannot be read") || !strings.Contains(why, "holds a sweep of") {
		t.Errorf("a record filed under the wrong key vouched for g.go (exit %d):\n%s", code, why)
	}
}

// record.go:99 and :92. A record that cannot be READ is refused -- a re-sweep
// replaces it -- where a record that is merely absent is "never swept".
func TestAnUnreadableRecordRefusesRatherThanVouches(t *testing.T) {
	store := t.TempDir()
	t.Setenv(recordsEnv, store)
	work := newPushRepo(t)
	key := committedKey(t, work, "g.go")
	// A directory where the record's file should be: a read error that is not
	// "does not exist".
	if err := os.MkdirAll(filepath.Join(store, key+".json"), 0o755); err != nil {
		t.Fatal(err)
	}
	// ⚠ The reason too: without record.go:99 the empty read is parsed as JSON
	// and refused as "unexpected end of JSON input" -- the same refusal, the
	// wrong cause. That version of this test let the guard survive a sweep.
	if code, _, why := hookOn(t, work, "gitpush"); code != 2 || !strings.Contains(why, "cannot be read") ||
		!strings.Contains(why, "is a directory") {
		t.Errorf("an unreadable record did not refuse for its own reason (exit %d):\n%s", code, why)
	}

	// And a store that cannot even be LOCATED: no override, and no home for
	// the user config directory.
	t.Setenv(recordsEnv, "")
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("AppData", "")
	if code, _, why := hookOn(t, work, "gitpush"); code != 2 || !strings.Contains(why, "cannot be read") {
		t.Errorf("a store that cannot be located did not refuse (exit %d):\n%s", code, why)
	}
}

// hook.go:189, :200, :235, :243. Where the gate cannot READ, it allows -- and
// says NOT JUDGED to the user, every time. An unannounced allowance would be
// the gate-that-does-nothing.
func TestWhereTheGateCannotReadItSaysNotJudged(t *testing.T) {
	t.Setenv(recordsEnv, t.TempDir())

	t.Run("not a repository", func(t *testing.T) { // :189
		// ⚠ The REASON, not just the outcome. Without :189 the next git call
		// fails too and the user is still told NOT JUDGED -- but told "no
		// origin to compare with", which sends them to the wrong place. A sweep
		// of the first version of this test proved it: the guard survived.
		code, msg, _ := hookOn(t, t.TempDir(), "gitpush")
		if code != 0 || !strings.Contains(msg, "NOT JUDGED") || !strings.Contains(msg, "rev-parse") {
			t.Errorf("exit %d, %q", code, msg)
		}
	})
	t.Run("no origin to compare with", func(t *testing.T) { // :200
		work := newPushRepo(t)
		gitIn(t, work, "remote", "remove", "origin")
		code, msg, _ := hookOn(t, work, "gitpush")
		if code != 0 || !strings.Contains(msg, "NOT JUDGED") || !strings.Contains(msg, "origin/main") {
			t.Errorf("exit %d, %q", code, msg)
		}
	})
	t.Run("committed but missing from the working tree", func(t *testing.T) { // :235
		work := newPushRepo(t)
		if err := os.Remove(filepath.Join(work, "g.go")); err != nil {
			t.Fatal(err)
		}
		code, msg, _ := hookOn(t, work, "gitpush")
		if code != 0 || !strings.Contains(msg, "NOT JUDGED g.go") {
			t.Errorf("exit %d, %q", code, msg)
		}
	})
	t.Run("committed source that does not parse", func(t *testing.T) { // :243
		work := newPushRepo(t)
		gitIn(t, work, "checkout", "main")
		gitIn(t, work, "checkout", "-b", "broken")
		if err := os.WriteFile(filepath.Join(work, "bad.go"), []byte("package g\n\nfunc (\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, work, "add", "bad.go")
		gitIn(t, work, "commit", "-m", "does not parse")
		code, msg, _ := hookOn(t, work, "gitpush")
		if code != 0 || !strings.Contains(msg, "NOT JUDGED bad.go") {
			t.Errorf("exit %d, %q", code, msg)
		}
	})
}

// hook.go:283. With no go.mod anywhere, the walk up for a module root must stop
// at the repository root -- without that exit it never stops.
func TestTheRemedyTerminatesWithoutAGoMod(t *testing.T) {
	t.Setenv(recordsEnv, t.TempDir())
	work := newPushRepo(t)
	gitIn(t, work, "rm", "-q", "go.mod")
	gitIn(t, work, "commit", "-m", "no module file")
	// git reports the RESOLVED path, and a temporary directory is often reached
	// through a link (/var -> /private/var on macOS): compare the same spelling.
	root, err := filepath.EvalSymlinks(work)
	if err != nil {
		t.Fatal(err)
	}
	code, _, why := hookOn(t, work, "gitpush")
	if code != 2 || !strings.Contains(why, "cd "+root+" && mutsweep -dir . ") {
		t.Errorf("the remedy did not fall back to the repository root (exit %d):\n%s", code, why)
	}
}

// record.go:119 and :130. A sweep whose record cannot be filed FAILS: a clean
// exit would send the reader looking for the fault in the gate that then
// refuses the push.
func TestASweepWhoseRecordCannotBeFiledFails(t *testing.T) {
	work := newPushRepo(t)
	bin := buildMutate(t)
	sweepWith := func(store string) (int, string) {
		t.Setenv(recordsEnv, store)
		var out, errOut bytes.Buffer
		c := run([]string{"-dir", work, "-mutate-bin", bin, "--", "go", "test", "./..."}, &out, &errOut)
		return c, errOut.String()
	}

	// :119 -- the store is a FILE, so it cannot be made a directory.
	notADir := filepath.Join(t.TempDir(), "store")
	if err := os.WriteFile(notADir, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// ⚠ This holds the MkdirAll at record.go:122 -- which no sweep can confirm:
	// it is an `if err := ...; err != nil`, and mutsweep does not mutate an if
	// with an init statement (go-fleettools/mutate#21). Asserted anyway, on the
	// step that failed, and said here rather than claimed as swept.
	if c, why := sweepWith(notADir); c != 1 || !strings.Contains(why, "NOT filed") || !strings.Contains(why, "mkdir") {
		t.Errorf("a store that is a file: exit %d, want 1 saying NOT filed at mkdir:\n%s", c, why)
	}

	// :119 -- the store cannot even be LOCATED. Without the guard, MkdirAll("")
	// fails next and the sweep is still refused, for a reason about an empty
	// path instead of the missing home: the cause is the assertion.
	// ⛔ An earlier version of this comment said :119 was the MkdirAll. It was
	// written against a line number that had not been read.
	for _, v := range []string{"GOCACHE", "GOPATH", "GOMODCACHE"} {
		// go itself needs these when HOME is gone, or the baseline run fails
		// and the sweep is refused before it reaches a record.
		out, err := exec.Command("go", "env", v).Output()
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv(v, strings.TrimSpace(string(out)))
	}
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	if c, why := sweepWith(""); c != 1 || !strings.Contains(why, "NOT filed") || !strings.Contains(why, "HOME") {
		t.Errorf("a store that cannot be located: exit %d, want 1 saying NOT filed for want of HOME:\n%s", c, why)
	}

	// :130 -- the store exists and cannot be written into.
	readOnly := t.TempDir()
	if err := os.Chmod(readOnly, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(readOnly, 0o700) })
	if c, why := sweepWith(readOnly); c != 1 || !strings.Contains(why, "NOT filed") {
		t.Errorf("a read-only store: exit %d, want 1 saying NOT filed:\n%s", c, why)
	}
}

// record.go:194. An -only run that meets a record it cannot read files nothing
// and fails, rather than writing a fresh record from a handful of lines.
func TestAnOnlyRunOverACorruptRecordFails(t *testing.T) {
	store := t.TempDir()
	t.Setenv(recordsEnv, store)
	work := newPushRepo(t)
	key := committedKey(t, work, "g.go")
	writeRaw(t, store, key, "{not a record")
	bin := buildMutate(t)
	var out, errOut bytes.Buffer
	c := run([]string{"-dir", work, "-only", "g.go:7", "-mutate-bin", bin, "--", "go", "test", "./..."}, &out, &errOut)
	if c != 1 || !strings.Contains(errOut.String(), "NOT filed") {
		t.Errorf("-only over a corrupt record: exit %d, want 1 saying NOT filed:\n%s", c, errOut.String())
	}
}
