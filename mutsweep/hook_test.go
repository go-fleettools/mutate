package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain keeps every sweep in this package away from the real record store.
//
// ⛔ The other tests in this package run REAL sweeps, and a sweep now files a
// record. Without this, `go test ./...` here would write records of temporary
// test packages into the user's own store -- harmless to the gate, since their
// bytes are nobody's, but a test that writes into the machine it runs on is a
// test that depends on it.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mutsweep-records-")
	if err != nil {
		panic(err)
	}
	os.Setenv(recordsEnv, dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestPushesInFindsEveryGitpushAndWhereItRuns(t *testing.T) {
	home, _ := os.UserHomeDir()
	// The platform's spelling of a path the SHELL calls /x: on Windows `/b`
	// becomes `\b`. Expectations written as "/b" were right on two of three CI
	// lanes and wrong on the third.
	p := func(s string) string { return filepath.Clean(filepath.FromSlash(s)) }
	for _, tc := range []struct {
		cmd, cwd string
		want     []push
	}{
		{"gitpush -u origin x", "/a", []push{{dir: "/a"}}},
		{"cd /b && gitpush origin x", "/a", []push{{dir: p("/b")}}},
		{"cd sub && gitpush", "/a", []push{{dir: p("/a/sub")}}},
		{"ls; cd /c; gitpush", "/a", []push{{dir: p("/c")}}},
		{"cd ~/clone && ~/.local/bin/gitpush -u origin x", "/a", []push{{dir: filepath.Join(home, "clone")}}},
		{"cd /b && MUTSWEEP_SKIP=1 gitpush", "/a", []push{{dir: p("/b"), skip: true}}},
		// Only =1 is the waiver: a variable that is merely PRESENT is not a decision.
		{"MUTSWEEP_SKIP=0 gitpush", "/a", []push{{dir: "/a"}}},
		{"GITSAFE_ALLOW_STALE_BASE=1 gitpush", "/a", []push{{dir: "/a"}}},
		// #26: HEAD moved earlier in the same line. The first is the line that
		// went through unjudged on go-tex/engine par-token-runaway.
		{"git add e.go && git commit -q -F m.txt && ~/.local/bin/gitpush -u origin x", "/a", []push{{dir: "/a", moved: "git commit"}}},
		{"git -C /r -c core.x=y cherry-pick abc; gitpush", "/a", []push{{dir: "/a", moved: "git cherry-pick"}}},
		{"cd /b && git switch -c y && MUTSWEEP_SKIP=1 gitpush", "/a", []push{{dir: p("/b"), skip: true, moved: "git switch"}}},
		// After the push, or not moving HEAD, it is not counted.
		{"gitpush && git commit -m z", "/a", []push{{dir: "/a"}}},
		{"git status && git log -1 && git fetch && gitpush", "/a", []push{{dir: "/a"}}},
		{"git -C commit status && gitpush", "/a", []push{{dir: "/a"}}},
		// Only GIT moves HEAD: another command whose second word is a git verb
		// is not one (mutsweep found this unheld: hook.go's "is it git" test).
		{"echo commit && gitpush", "/a", []push{{dir: "/a"}}},
		// Not pushes: the word appears, but not as the command.
		{"echo gitpush", "/a", nil},
		{"git push origin x", "/a", nil},
		{"grep -n gitpush notes.txt", "/a", nil},
		{"", "/a", nil},
	} {
		got := pushesIn(tc.cmd, tc.cwd)
		if len(got) != len(tc.want) {
			t.Errorf("%q: %d pushes %+v, want %+v", tc.cmd, len(got), got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%q: push %d = %+v, want %+v", tc.cmd, i, got[i], tc.want[i])
			}
		}
	}
}

// #26 end to end: a commit and a push on one line is refused with the reason,
// even though HEAD (not yet committed) has nothing for the gate to judge; the
// waiver still waives it, said.
func TestACommitThenPushOnOneLineIsRefusedWithTheReason(t *testing.T) {
	t.Setenv(recordsEnv, t.TempDir())
	work := newPushRepo(t)
	code, _, why := hookOn(t, work, "git commit -qam x && gitpush")
	if code != 2 || !strings.Contains(why, "runs `git commit` before it pushes") || !strings.Contains(why, "Run the push as a command of its own") {
		t.Fatalf("commit-then-push was not refused with its reason (exit %d):\n%s", code, why)
	}
	code, msg, _ := hookOn(t, work, "git commit -qam x && MUTSWEEP_SKIP=1 gitpush")
	if code != 0 || !strings.Contains(msg, "WAIVED") || !strings.Contains(msg, "git commit") {
		t.Fatalf("the waiver did not waive it, said (exit %d): %q", code, msg)
	}
}

func TestIsAssignment(t *testing.T) {
	for w, want := range map[string]bool{
		"A=1": true, "_x=": true, "GITSAFE_ALLOW_X=1": true, "a9=b": true,
		"=1": false, "9a=b": false, "a-b=c": false, "gitpush": false, "--x=1": false,
	} {
		if got := isAssignment(w); got != want {
			t.Errorf("isAssignment(%q) = %v, want %v", w, got, want)
		}
	}
}

// A command that does not push is answered in silence: the hook runs before
// every Bash call on the machine, and anything it prints is noise on all of them.
func TestANonPushIsAllowedInSilence(t *testing.T) {
	for _, payload := range []string{
		`{"tool_name":"Bash","tool_input":{"command":"ls -la"},"cwd":"/"}`,
		`{"tool_name":"Edit","tool_input":{"command":"gitpush"},"cwd":"/"}`,
	} {
		var out, errOut bytes.Buffer
		if code := hookMain(strings.NewReader(payload), &out, &errOut); code != 0 {
			t.Errorf("%s: exit %d, want 0", payload, code)
		}
		if out.Len()+errOut.Len() != 0 {
			t.Errorf("%s: printed %q / %q, want nothing", payload, out.String(), errOut.String())
		}
	}
}

// ⛔ When it cannot read, it allows -- and SAYS so. An allowance that is not
// announced is the gate-that-does-nothing again.
func TestAnUnreadablePayloadIsAllowedButSaidToBeUnjudged(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := hookMain(strings.NewReader("{not json"), &out, &errOut); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if !strings.Contains(out.String(), "NOT JUDGED") || !strings.Contains(out.String(), "systemMessage") {
		t.Errorf("an unreadable payload was allowed in silence: %q", out.String())
	}
}

// --- end to end -----------------------------------------------------------

// gitIn runs git with hooks and signing turned off: the machine's own global
// hooks (gitsafe's pre-push and post-checkout guards) are about real clones, and
// a test repository must not depend on them either way.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "core.hooksPath=" + os.DevNull, "-c", "commit.gpgsign=false",
		"-c", "user.email=test@example.invalid", "-c", "user.name=test", "-C", dir}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

const guardedOne = `package g

import "errors"

// Check refuses a negative.
func Check(n int) error {
	if n < 0 {
		return errors.New("negative")
	}
	return nil
}
`

const guardedTwo = `package g

import "errors"

// Check refuses a negative, and anything over a hundred.
func Check(n int) error {
	if n < 0 {
		return errors.New("negative")
	}
	if n > 100 {
		return errors.New("big")
	}
	return nil
}
`

const heldTest = `package g

import "testing"

func TestCheck(t *testing.T) {
	if Check(-1) == nil {
		t.Fatal("a negative was accepted")
	}
	if Check(101) == nil {
		t.Fatal("101 was accepted")
	}
	if Check(5) != nil {
		t.Fatal("5 was refused")
	}
}
`

// newPushRepo makes an origin and a clone whose main holds one guard, then a
// feature branch adding a second one, committed: the state just before a push.
func newPushRepo(t *testing.T) (work string) {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	work = filepath.Join(root, "work")
	gitIn(t, root, "init", "--bare", "-b", "main", origin)
	gitIn(t, root, "init", "-b", "main", work)
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(work, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/g\n\ngo 1.27\n")
	write("g.go", guardedOne)
	write("g_test.go", unheldTest)
	gitIn(t, work, "add", ".")
	gitIn(t, work, "commit", "-m", "one guard, held")
	gitIn(t, work, "remote", "add", "origin", origin)
	gitIn(t, work, "push", "origin", "main")
	gitIn(t, work, "checkout", "-b", "feature")
	write("g.go", guardedTwo)
	write("g_test.go", heldTest)
	gitIn(t, work, "commit", "-am", "a second guard, held too")
	return work
}

func hookOn(t *testing.T, work, command string) (int, string, string) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{
		"tool_name": "Bash", "cwd": work,
		"tool_input": map[string]string{"command": command},
	})
	var out, errOut bytes.Buffer
	code := hookMain(bytes.NewReader(b), &out, &errOut)
	return code, out.String(), errOut.String()
}

// ⭐ THE GATE'S WHOLE LIFE, against a real repository and a real sweep: refused
// before the sweep, allowed after it, refused again once the file changes, and
// waivable only in public.
func TestTheGateRefusesUntilASweepCoversTheBytesPushed(t *testing.T) {
	t.Setenv(recordsEnv, t.TempDir())
	work := newPushRepo(t)
	bin := buildMutate(t)

	code, _, why := hookOn(t, work, "gitpush -u origin feature")
	if code != 2 {
		t.Fatalf("an unswept change was allowed (exit %d)", code)
	}
	// Only line 10 is the push's own: line 7's guard is main's (#24), and the
	// remedy asks about line 10 alone.
	for _, want := range []string{"g.go", "1 changed refusal(s) never swept as committed: g.go:10", "mutsweep -dir . -run-in . -only g.go:10"} {
		if !strings.Contains(why, want) {
			t.Errorf("the refusal does not say %q:\n%s", want, why)
		}
	}

	var out, errOut bytes.Buffer
	if c := run([]string{"-dir", work, "-mutate-bin", bin, "--", "go", "test", "./..."}, &out, &errOut); c != 0 {
		t.Fatalf("the sweep of a fully held file exited %d:\n%s\n%s", c, out.String(), errOut.String())
	}
	if code, msg, why := hookOn(t, work, "gitpush -u origin feature"); code != 0 || msg != "" || why != "" {
		t.Fatalf("a swept change was not allowed in silence: exit %d, %q, %q", code, msg, why)
	}

	// Any edit is a new file as far as the record is concerned -- a comment
	// included. A record cannot vouch for bytes it never saw.
	if err := os.WriteFile(filepath.Join(work, "g.go"), []byte(guardedTwo+"\n// edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "commit", "-am", "a comment")
	if code, _, _ := hookOn(t, work, "gitpush"); code != 2 {
		t.Errorf("an edit after the sweep was allowed (exit %d): the record vouched for bytes it never saw", code)
	}
	code, msg, _ := hookOn(t, work, "MUTSWEEP_SKIP=1 gitpush")
	if code != 0 {
		t.Fatalf("the waiver was refused (exit %d)", code)
	}
	if !strings.Contains(msg, "WAIVED") || !strings.Contains(msg, "g.go") {
		t.Errorf("the waiver was not said to the user, naming the file: %q", msg)
	}
}

// ⛔ The gate judges what HEAD COMMITS, not the working tree: an uncommitted
// edit must not decide whether a commit is covered.
func TestTheGateJudgesTheCommittedBytesNotTheWorkingTree(t *testing.T) {
	t.Setenv(recordsEnv, t.TempDir())
	work := newPushRepo(t)
	bin := buildMutate(t)
	var out, errOut bytes.Buffer
	if c := run([]string{"-dir", work, "-mutate-bin", bin, "--", "go", "test", "./..."}, &out, &errOut); c != 0 {
		t.Fatalf("sweep exited %d:\n%s", c, errOut.String())
	}
	// An uncommitted edit: the committed bytes are still the swept ones.
	if err := os.WriteFile(filepath.Join(work, "g.go"), []byte(guardedTwo+"\n// not committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, why := hookOn(t, work, "gitpush"); code != 0 {
		t.Errorf("an uncommitted edit refused a covered commit (exit %d):\n%s", code, why)
	}
}

const unheldTest = `package g

import "testing"

func TestCheck(t *testing.T) {
	if Check(-1) == nil {
		t.Fatal("a negative was accepted")
	}
}
`

// A sweep that FINDS a survivor files it, and the gate refuses naming the line.
// Then a test is written, -only re-asks that one line, and the gate clears
// without a full re-sweep -- the loop the report itself recommends.
func TestASurvivorRefusesUntilATestHoldsItAndOnlyReasksIt(t *testing.T) {
	t.Setenv(recordsEnv, t.TempDir())
	work := newPushRepo(t)
	if err := os.WriteFile(filepath.Join(work, "g_test.go"), []byte(unheldTest), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "commit", "-am", "a test that holds only the first guard")
	bin := buildMutate(t)

	var out, errOut bytes.Buffer
	if c := run([]string{"-dir", work, "-mutate-bin", bin, "--", "go", "test", "./..."}, &out, &errOut); c != 1 {
		t.Fatalf("a sweep with a survivor exited %d, want 1:\n%s", c, errOut.String())
	}
	code, _, why := hookOn(t, work, "gitpush")
	if code != 2 || !strings.Contains(why, "SURVIVED g.go:10") || !strings.Contains(why, "n > 100") {
		t.Fatalf("the survivor did not refuse the push by name (exit %d):\n%s", code, why)
	}

	// The test the survivor asked for. g.go itself is untouched, so its record
	// still applies and -only can update it.
	if err := os.WriteFile(filepath.Join(work, "g_test.go"), []byte(heldTest), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "commit", "-am", "hold the second guard")
	out.Reset()
	errOut.Reset()
	if c := run([]string{"-dir", work, "-only", "g.go:10", "-mutate-bin", bin, "--", "go", "test", "./..."}, &out, &errOut); c != 0 {
		t.Fatalf("-only on the held survivor exited %d:\n%s", c, errOut.String())
	}
	if code, _, why := hookOn(t, work, "gitpush"); code != 0 {
		t.Errorf("-only cleared the survivor but the gate still refuses (exit %d):\n%s", code, why)
	}
}

// A change to tests alone sends no guarded file, and is not judged.
func TestATestOnlyChangeIsAllowed(t *testing.T) {
	t.Setenv(recordsEnv, t.TempDir())
	work := newPushRepo(t)
	gitIn(t, work, "checkout", "main")
	gitIn(t, work, "checkout", "-b", "tests-only")
	if err := os.WriteFile(filepath.Join(work, "g_test.go"), []byte(heldTest+"\n// more\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "commit", "-am", "tests only")
	if code, msg, why := hookOn(t, work, "gitpush"); code != 0 || msg != "" || why != "" {
		t.Errorf("a test-only change was judged: exit %d, %q, %q", code, msg, why)
	}
}
