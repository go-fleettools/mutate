package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #24: the gate judges the refusals a push CHANGES. Measured on its first real
// push (go-tex/engine), one unconditional line added to a 97-guard file asked
// for a sweep of all 97, every one of them older than the push.

// A push that changes a guarded file without touching any of its guards owes no
// sweep at all -- even though nothing has ever swept the file.
func TestAPushThatTouchesNoGuardIsNotAskedToSweepTheFile(t *testing.T) {
	t.Setenv(recordsEnv, t.TempDir())
	work := newPushRepo(t)
	// Move main to the two-guard file, then branch an edit of the doc comment.
	gitIn(t, work, "checkout", "main")
	if err := os.WriteFile(filepath.Join(work, "g.go"), []byte(guardedTwo), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "commit", "-am", "two guards on main")
	gitIn(t, work, "push", "origin", "main")
	gitIn(t, work, "checkout", "-b", "doc")
	edited := strings.Replace(guardedTwo, "anything over a hundred", "anything above one hundred", 1)
	if err := os.WriteFile(filepath.Join(work, "g.go"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "commit", "-am", "reword the comment")
	if code, msg, why := hookOn(t, work, "gitpush -u origin doc"); code != 0 || msg != "" || why != "" {
		t.Fatalf("a comment edit was asked to cover its file's old guards: exit %d, %q, %q", code, msg, why)
	}

	// The control: the same branch then CHANGES a guard, and is refused for
	// that one line only.
	changed := strings.Replace(edited, "n > 100", "n > 99", 1)
	if err := os.WriteFile(filepath.Join(work, "g.go"), []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "commit", "-am", "move the bound")
	code, _, why := hookOn(t, work, "gitpush")
	if code != 2 || !strings.Contains(why, "never swept as committed: g.go:10") || strings.Contains(why, "g.go:7") {
		t.Fatalf("a changed guard was not refused for its own line alone (exit %d):\n%s", code, why)
	}
}

// The remedy the refusal prints works from nothing: -only over bytes that no
// sweep ever saw files a record for exactly those lines, which clears the push
// -- and does not vouch for a changed line it was not asked about.
func TestOnlyStartsARecordThatCoversJustItsLines(t *testing.T) {
	t.Setenv(recordsEnv, t.TempDir())
	work := newPushRepo(t) // main: guard on line 7; feature adds line 10
	bin := buildMutate(t)

	// Change line 7 too, so the push owns BOTH guards.
	both := strings.Replace(guardedTwo, "n < 0", "n < 1", 1)
	if err := os.WriteFile(filepath.Join(work, "g.go"), []byte(both), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "commit", "-am", "move the first bound too")
	var out, errOut bytes.Buffer
	if c := run([]string{"-dir", work, "-only", "g.go:10", "-mutate-bin", bin, "--", "go", "test", "./..."}, &out, &errOut); c != 0 {
		t.Fatalf("-only g.go:10 exited %d:\n%s\n%s", c, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "filed one for the 1 line(s) asked about") {
		t.Errorf("-only did not say it started a partial record:\n%s", errOut.String())
	}
	code, _, why := hookOn(t, work, "gitpush")
	if code != 2 || !strings.Contains(why, "never swept as committed: g.go:7") || strings.Contains(why, "g.go:10") {
		t.Fatalf("a record of line 10 vouched for line 7, or line 10 was still asked for (exit %d):\n%s", code, why)
	}

	// Asking about line 7 as well ADDS to the record: both are covered now.
	out.Reset()
	errOut.Reset()
	if c := run([]string{"-dir", work, "-only", "g.go:7", "-mutate-bin", bin, "--", "go", "test", "./..."}, &out, &errOut); c != 0 {
		t.Fatalf("-only g.go:7 exited %d:\n%s\n%s", c, out.String(), errOut.String())
	}
	if code, msg, why := hookOn(t, work, "gitpush"); code != 0 || msg != "" || why != "" {
		t.Fatalf("both changed guards swept, but the push is not allowed in silence: exit %d, %q, %q", code, msg, why)
	}
}

func TestNewSideReadsTheHeadHalfOfAHunkHeader(t *testing.T) {
	for _, c := range []struct {
		header       string
		start, count int
	}{
		{"@@ -1,2 +3,4 @@ func f() {", 3, 4},
		{"@@ -7 +7 @@", 7, 1},     // no count: one line
		{"@@ -9,2 +8,0 @@", 8, 0}, // a pure deletion changes no HEAD line
		{"@@ -0,0 +1,12 @@", 1, 12},
	} {
		s, n, err := newSide(c.header)
		if err != nil || s != c.start || n != c.count {
			t.Errorf("newSide(%q) = %d, %d, %v; want %d, %d", c.header, s, n, err, c.start, c.count)
		}
	}
	for _, bad := range []string{"@@", "@@ -1,2 3,4 @@", "@@ -1 +x @@", "@@ -1 +3,y @@"} {
		if _, _, err := newSide(bad); err == nil {
			t.Errorf("newSide(%q) accepted a header it cannot read", bad)
		}
	}
}
