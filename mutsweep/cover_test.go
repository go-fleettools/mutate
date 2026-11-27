package main

// ⛔ THESE TESTS EXIST FOR ISSUE #33: mutsweep was measured at 92.6% of its own
// statements, and `mutsweep -hook` runs before every Bash call of every session
// on the fleet. Each test below holds a block that NO test reached, and each one
// says which. The seams they use (osExit, gitOut, relPath, createRecordTemp) are
// there for them and change nothing a user can see.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// ---- hook.go -------------------------------------------------------------

// git's own leading options are skipped, and a line that never reaches a
// subcommand moves nothing.
func TestHeadMoverSkipsOptionsAndAnswersNothingWithoutASubcommand(t *testing.T) {
	for _, tt := range []struct {
		words []string
		want  string
	}{
		{[]string{"git", "--no-pager", "commit"}, "commit"},
		{[]string{"git", "-C", "dir", "push"}, ""},
		{[]string{"git", "-C", "dir", "reset"}, "reset"},
		{[]string{"git", "--no-pager"}, ""},
		{[]string{"git", "-C", "dir"}, ""},
		{[]string{"git"}, ""},
		{nil, ""},
		{[]string{"echo", "commit"}, ""},
	} {
		if got := headMover(tt.words); got != tt.want {
			t.Errorf("headMover(%q) = %q, want %q", tt.words, got, tt.want)
		}
	}
}

// testdata/ and vendor/ are not built by the go tool, at any depth.
func TestUnderSkippedDir(t *testing.T) {
	for _, tt := range []struct {
		path string
		want bool
	}{
		{"a/testdata/x.go", true},
		{"vendor/x.go", true},
		{"a/b/vendor/c/x.go", true},
		{"testdata", true},
		{"a/b/x.go", false},
		{"a/testdatax/x.go", false},
		{"x.go", false},
	} {
		if got := underSkippedDir(tt.path); got != tt.want {
			t.Errorf("underSkippedDir(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

// withGit lets a test answer chosen git invocations itself and leaves the rest
// to the real git.
func withGit(t *testing.T, f func(args []string) ([]byte, error, bool)) {
	t.Helper()
	real := gitOut
	t.Cleanup(func() { gitOut = real })
	gitOut = func(dir string, args ...string) ([]byte, error) {
		if out, err, ok := f(args); ok {
			return out, err
		}
		return real(dir, args...)
	}
}

// hook.go: the diff that follows a merge-base which worked. Where git cannot
// say, the gate allows and SAYS so.
func TestAFailedFileListIsNotJudgedSilently(t *testing.T) {
	t.Setenv(recordsEnv, t.TempDir())
	work := newPushRepo(t)
	withGit(t, func(args []string) ([]byte, error, bool) {
		if len(args) > 1 && args[0] == "diff" && args[1] == "--name-only" {
			return nil, errors.New("the diff failed"), true
		}
		return nil, nil, false
	})
	code, msg, why := hookOn(t, work, "gitpush")
	if code != 0 || why != "" || !strings.Contains(msg, "NOT JUDGED") || !strings.Contains(msg, "the diff failed") {
		t.Errorf("exit %d, %q, %q: a file list git could not give was not said to be unjudged", code, msg, why)
	}
}

// hook.go: a file the diff just listed that HEAD cannot show.
func TestAFileHeadCannotShowIsNotJudgedSilently(t *testing.T) {
	t.Setenv(recordsEnv, t.TempDir())
	work := newPushRepo(t)
	withGit(t, func(args []string) ([]byte, error, bool) {
		if len(args) > 0 && args[0] == "show" {
			return nil, errors.New("no such blob"), true
		}
		return nil, nil, false
	})
	code, msg, why := hookOn(t, work, "gitpush")
	if code != 0 || why != "" || !strings.Contains(msg, "NOT JUDGED g.go: ") || !strings.Contains(msg, "no such blob") {
		t.Errorf("exit %d, %q, %q: a blob git could not show was not said to be unjudged", code, msg, why)
	}
}

// hook.go: a hunk header that cannot be read, in the diff changedLines asks for.
func TestAHunkHeaderThatCannotBeReadIsNotJudgedSilently(t *testing.T) {
	t.Setenv(recordsEnv, t.TempDir())
	work := newPushRepo(t)
	withGit(t, func(args []string) ([]byte, error, bool) {
		if len(args) > 1 && args[0] == "diff" && args[1] == "-U0" {
			return []byte("@@ -1 @@ junk\n"), nil, true
		}
		return nil, nil, false
	})
	code, msg, why := hookOn(t, work, "gitpush")
	if code != 0 || why != "" || !strings.Contains(msg, "NOT JUDGED g.go: ") || !strings.Contains(msg, "not a hunk header") {
		t.Errorf("exit %d, %q, %q: an unreadable hunk header was not said to be unjudged", code, msg, why)
	}
}

// hook.go: a file the native build leaves out cannot be judged by a native
// sweep, so a push of one is not refused for want of a record no sweep can write.
func TestAFileTheNativeBuildExcludesIsNotJudged(t *testing.T) {
	t.Setenv(recordsEnv, t.TempDir())
	work := newPushRepo(t)
	gitIn(t, work, "checkout", "main")
	gitIn(t, work, "checkout", "-b", "tagged")
	body := "//go:build neverbuilt\n\n" + strings.Replace(guardedTwo, "Check", "Other", -1)
	if err := os.WriteFile(filepath.Join(work, "tagged.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "add", "tagged.go")
	gitIn(t, work, "commit", "-m", "a file no native build compiles")
	if code, msg, why := hookOn(t, work, "gitpush"); code != 0 || msg != "" || why != "" {
		t.Errorf("a file the build excludes was judged: exit %d, %q, %q", code, msg, why)
	}
}

// panicRecord files a full sweep of the committed g.go whose verdicts are the
// given ones, the way a sweep would have.
func panicRecord(t *testing.T, work string, needed []entry) {
	t.Helper()
	src := []byte(gitIn(t, work, "show", "HEAD:g.go"))
	if err := storeRecord(record{SHA256: contentKey(src), File: "g.go", Guards: 2, Needed: needed, At: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
}

// hook.go: a verdict on a refusal the push did not touch is not the push's to
// answer for, and a PANIC red on one it did is allowed (it is said only
// beside a refusal).
func TestAVerdictOnALineThePushDidNotTouchIsNotItsOwn(t *testing.T) {
	t.Setenv(recordsEnv, t.TempDir())
	work := newPushRepo(t)
	// Line 7's guard is main's; line 10 is the push's.
	panicRecord(t, work, []entry{
		{Line: 7, Verdict: survived, Cond: "n < 0"},
		{Line: 10, Verdict: caughtByPanic, Cond: "n > 100"},
	})
	if code, msg, why := hookOn(t, work, "gitpush"); code != 0 || msg != "" || why != "" {
		t.Errorf("a survivor on main's own line, or a panic red, refused the push: exit %d, %q, %q", code, msg, why)
	}
}

// hook.go: beside a refusal, the PANIC reds are listed as not refused; and what
// the gate could not judge in ANOTHER repository is still said.
func TestARefusalNamesThePanicsAndWhatItCouldNotJudge(t *testing.T) {
	t.Setenv(recordsEnv, t.TempDir())
	work := newPushRepo(t)
	rewritten := strings.Replace(strings.Replace(guardedTwo, "n < 0", "n <= -1", 1), "n > 100", "n >= 101", 1)
	if err := os.WriteFile(filepath.Join(work, "g.go"), []byte(rewritten), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "commit", "-am", "both guards rewritten")
	panicRecord(t, work, []entry{
		{Line: 7, Verdict: survived, Cond: "n <= -1"},
		{Line: 10, Verdict: caughtByPanic, Cond: "n >= 101"},
	})
	elsewhere := t.TempDir() // not a repository
	code, _, why := hookOn(t, work, "cd "+work+" && gitpush ; cd "+elsewhere+" && gitpush")
	if code != 2 {
		t.Fatalf("a survivor on a changed line was allowed (exit %d):\n%s", code, why)
	}
	for _, want := range []string{"SURVIVED g.go:7", "caught by PANIC, NOT refused", "g.go:10", "NOT JUDGED in " + elsewhere} {
		if !strings.Contains(why, want) {
			t.Errorf("the refusal does not say %q:\n%s", want, why)
		}
	}
	if strings.Contains(why, "g.go:7)") {
		t.Errorf("the survivor was listed among the panics:\n%s", why)
	}
}

// hook.go: where the path from the module root cannot be made, the remedy names
// the directory in full instead of nothing.
func TestTheRemedyNamesTheDirectoryWhenNoRelativePathExists(t *testing.T) {
	real := relPath
	t.Cleanup(func() { relPath = real })
	relPath = func(string, string) (string, error) { return "", errors.New("different volumes") }
	root := t.TempDir()
	v := verdictOf{root: root, refused: map[string][]string{"g.go": {"why"}}, only: map[string][]string{}}
	got := remedy(v)
	if len(got) != 1 || !strings.Contains(got[0], "-dir "+root+" ") {
		t.Errorf("remedy = %q, want -dir to be the absolute %s", got, root)
	}
}

// ---- guards.go -----------------------------------------------------------

func TestNounIsWhatTheModeCounts(t *testing.T) {
	if got := (config{mode: modeBoundary}).noun(); got != "comparisons" {
		t.Errorf("boundary noun = %q", got)
	}
	if got := (config{mode: modeDelete}).noun(); got != "refusals" {
		t.Errorf("delete noun = %q", got)
	}
}

func TestParseRefusesWhatItCannotRead(t *testing.T) {
	var errOut bytes.Buffer
	if _, _, code := parse([]string{"-no-such-flag"}, &errOut); code != 2 {
		t.Errorf("an unknown flag exited %d, want 2", code)
	}
	errOut.Reset()
	if _, _, code := parse([]string{"-only", "nonsense", "--", "true"}, &errOut); code != 2 || !strings.Contains(errOut.String(), "file.go:line") {
		t.Errorf("a bad -only exited %d: %q", code, errOut.String())
	}
}

// guards.go:109. An unknown flag must stop the parse THERE: read on, the
// arguments after it are a perfectly good tally, and a run that exits 0 after
// being told something it did not understand is worse than one that exits 2.
func TestAnUnknownFlagStopsTheParseEvenWhenTheRestIsValid(t *testing.T) {
	var errOut bytes.Buffer
	if _, _, code := parse([]string{"-no-such-flag", "-tally"}, &errOut); code != 2 {
		t.Errorf("exit %d, want 2: the unknown flag was read past", code)
	}
}

// sweep.go:430. A command that cannot START is reported as that, not as the
// "not started" a Wait on it would say.
func TestACommandThatCannotStartSaysSo(t *testing.T) {
	c := exec.Command(filepath.Join(t.TempDir(), "no-such-program"))
	_, err := runBounded(context.Background(), c)
	if err == nil || strings.Contains(err.Error(), "not started") {
		t.Errorf("error = %v, want the start failure", err)
	}
}

// record.go:123. A record that is not JSON is reported as unreadable, naming
// its key; it must not fall through to the key comparison below, which would
// say something else (that it holds a sweep of nothing).
func TestARecordThatIsNotJSONIsReportedAsUnreadable(t *testing.T) {
	store := t.TempDir()
	t.Setenv(recordsEnv, store)
	write(t, store, "k.json", "{ not json")
	_, _, err := lookupRecord("k")
	if err == nil || !strings.Contains(err.Error(), "record k:") || strings.Contains(err.Error(), "holds a sweep") {
		t.Errorf("error = %v", err)
	}
}

// hook.go:396. The NEAREST go.mod wins: a module below the repository root is
// the module root, not the repository root.
func TestTheNearestGoModIsTheModuleRoot(t *testing.T) {
	root := t.TempDir()
	mod := filepath.Join(root, "a")
	deep := filepath.Join(mod, "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, mod, "go.mod", "module x\n\ngo 1.27\n")
	if got := moduleRoot(deep, root); got != mod {
		t.Errorf("moduleRoot = %q, want %q", got, mod)
	}
}

// guards.go: every way collect can fail on what it is pointed at.
func TestCollectSaysWhatItCouldNotRead(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := collect(config{dir: filepath.Join(dir, "absent")}); err == nil {
		t.Error("a directory that is not there was swept")
	}
	if _, _, err := collect(config{dir: dir, files: []string{"absent.go"}}); err == nil {
		t.Error("a file that is not there was swept")
	}
	write(t, dir, "broken.go", "package x\n\nfunc {\n")
	if _, _, err := collect(config{dir: dir}); err == nil || !strings.Contains(err.Error(), "broken.go") {
		t.Errorf("a file that does not parse was swept: %v", err)
	}
}

// guards.go: a file that passed the build check and then cannot be read.
func TestAFileThatCannotBeReadAfterTheBuildCheckIsAnError(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x.go", oneGuard)
	real := readSource
	t.Cleanup(func() { readSource = real })
	readSource = func(string) ([]byte, error) { return nil, errors.New("the disk went away") }
	if _, _, err := collect(config{dir: dir}); err == nil || !strings.Contains(err.Error(), "the disk went away") {
		t.Errorf("error = %v", err)
	}
}

// guards.go: the widening stops at the top of the file.
func TestUniqueWidensToTheStartOfTheFile(t *testing.T) {
	from, to := unique("ab ab", 3, 5)
	if from != "ab ab" || to != "ab " {
		t.Errorf("unique = %q, %q", from, to)
	}
	// And stops as soon as the text is unique, without widening further.
	from, to = unique("x\nab\ncd\nab\n", 8, 10)
	if from != "cd\nab" || to != "cd\n" {
		t.Errorf("unique = %q, %q", from, to)
	}
}

func TestPathFromFallsBackToTheJoinedPathWhenNoRelativeOneExists(t *testing.T) {
	abs := t.TempDir()
	// A relative path cannot be made relative to an absolute one.
	if got, want := pathFrom("rel", abs, "f.go"), filepath.Join("rel", "f.go"); got != want {
		t.Errorf("pathFrom = %q, want %q", got, want)
	}
	if got, want := pathFrom(filepath.Join(abs, "sub"), abs, "f.go"), filepath.Join("sub", "f.go"); got != want {
		t.Errorf("pathFrom = %q, want %q", got, want)
	}
}

// ---- main.go -------------------------------------------------------------

// record.go: the real creator reports a directory it cannot create in, and
// hands back NO file, not a nil *os.File inside a non-nil interface.
func TestCreatingARecordTempInAMissingDirectoryIsAnError(t *testing.T) {
	f, err := createRecordTemp(filepath.Join(t.TempDir(), "absent"), ".record-*")
	if err == nil {
		t.Fatal("no error for a directory that does not exist")
	}
	if f != nil {
		t.Errorf("returned %v alongside the error, want a nil interface", f)
	}
	ok, err := createRecordTemp(t.TempDir(), ".record-*")
	if err != nil || ok == nil {
		t.Fatalf("in a real directory: %v, %v", ok, err)
	}
	ok.Close()
	os.Remove(ok.Name())
}

// keepActive makes drive leave MUTSWEEP_ACTIVE as the test set it.
var keepActive bool

// drive runs main() with the given arguments and stdin, and returns what it
// asked the process to exit with and what it wrote to stderr.
func drive(t *testing.T, stdin string, args ...string) (exits []int, stderr string) {
	t.Helper()
	// These tests run main() in this process, and the suite itself may be the
	// command of a sweep, which sets MUTSWEEP_ACTIVE: unset unless the test is
	// about exactly that.
	if !keepActive {
		t.Setenv(active, "")
	}
	dir := t.TempDir()
	in, err := os.Create(filepath.Join(dir, "in"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.WriteString(stdin); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	errF, err := os.Create(filepath.Join(dir, "err"))
	if err != nil {
		t.Fatal(err)
	}
	outF, err := os.Create(filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}
	realArgs, realIn, realOut, realErr, realExit := os.Args, os.Stdin, os.Stdout, os.Stderr, osExit
	t.Cleanup(func() {
		os.Args, os.Stdin, os.Stdout, os.Stderr, osExit = realArgs, realIn, realOut, realErr, realExit
		in.Close()
		errF.Close()
		outF.Close()
	})
	os.Args, os.Stdin, os.Stdout, os.Stderr = append([]string{"mutsweep"}, args...), in, outF, errF
	// A fake that RETURNS, as os.Exit never does: what main does after asking to
	// exit is then part of what is asserted.
	osExit = func(code int) { exits = append(exits, code) }
	main()
	b, err := os.ReadFile(filepath.Join(dir, "err"))
	if err != nil {
		t.Fatal(err)
	}
	return exits, string(b)
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// main.go: the hook is answered first and ONCE, and nothing else runs after it.
func TestMainAnswersTheHookAndStops(t *testing.T) {
	if exits, errText := drive(t, "{}", "-hook"); !equalInts(exits, []int{0}) {
		t.Errorf("-hook asked for exits %v (stderr %q), want exactly [0]", exits, errText)
	}
	// A stray marker in a session's environment must not refuse the hook.
	t.Setenv(active, "1")
	if exits, errText := drive(t, "{}", "-hook"); !equalInts(exits, []int{0}) || errText != "" {
		t.Errorf("-hook under %s asked for exits %v (stderr %q), want exactly [0]", active, exits, errText)
	}
}

// main.go: -hook is the WHOLE command line. Any other arguments make it an
// ordinary run, and -hook is not a flag of that.
func TestMainReadsHookOnlyAsTheWholeCommandLine(t *testing.T) {
	if exits, _ := drive(t, "{}", "-hook", "-tally"); !equalInts(exits, []int{2}) {
		t.Errorf("-hook with another argument asked for exits %v, want [2] from the flag parser", exits)
	}
}

// main.go: a sweep inside a sweep is refused, once, with the reason.
func TestMainRefusesASweepInsideASweep(t *testing.T) {
	t.Setenv(active, "1")
	keepActive = true
	t.Cleanup(func() { keepActive = false })
	exits, errText := drive(t, "", "-tally", "-dir", t.TempDir())
	if !equalInts(exits, []int{2}) || !strings.Contains(errText, "inside another sweep") {
		t.Errorf("exits %v, stderr %q", exits, errText)
	}
}

// main.go: otherwise main hands the arguments to run, and exits with what it says.
func TestMainExitsWithWhatRunSays(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x.go", oneGuard)
	if exits, errText := drive(t, "", "-tally", "-dir", dir); !equalInts(exits, []int{0}) {
		t.Errorf("exits %v (stderr %q), want [0]", exits, errText)
	}
	if exits, _ := drive(t, "", "-tally", "-dir", filepath.Join(dir, "absent")); !equalInts(exits, []int{2}) {
		t.Errorf("exits %v, want [2]", exits)
	}
}

// main.go: run says which files the build left out, and still tallies the rest.
func TestRunSaysWhatTheBuildExcluded(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x.go", oneGuard)
	write(t, dir, "y.go", "//go:build neverbuilt\n\n"+strings.Replace(oneGuard, "One", "Two", 1))
	var out, errOut bytes.Buffer
	if code := run([]string{"-tally", "-dir", dir}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "not swept") || !strings.Contains(errOut.String(), "y.go") {
		t.Errorf("the excluded file was not named: %q", errOut.String())
	}
	if !strings.Contains(out.String(), "1 refusals in 1 file(s)") {
		t.Errorf("the tally is wrong: %q", out.String())
	}
}

func TestRunSaysWhyItCouldNotCollect(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"-tally", "-dir", filepath.Join(t.TempDir(), "absent")}, &out, &errOut)
	// The cause is named: a run that found nothing to sweep says another thing.
	if code != 2 || !strings.Contains(errOut.String(), "mutsweep:") || !strings.Contains(errOut.String(), "absent") {
		t.Errorf("exit %d: %q", code, errOut.String())
	}
}

// ---- record.go -----------------------------------------------------------

// record.go: with no override the store is under the user's CONFIG directory.
func TestTheStoreIsUnderTheUserConfigDirectoryByDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv(recordsEnv, "")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("AppData", filepath.Join(home, "appdata"))
	base, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	got, err := recordDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(base, "mutsweep", "records"); got != want {
		t.Errorf("recordDir = %q, want %q", got, want)
	}
}

// record.go: a record that cannot be encoded is an error, not a file.
func TestARecordThatCannotBeEncodedIsNotFiled(t *testing.T) {
	t.Setenv(recordsEnv, t.TempDir())
	// A year JSON cannot spell.
	err := storeRecord(record{SHA256: "k", At: time.Date(12000, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err == nil {
		t.Fatal("a record with an unencodable time was filed")
	}
	if _, _, lerr := lookupRecord("k"); lerr != nil {
		t.Errorf("a partial file was left: %v", lerr)
	}
}

// failingTemp is a real temporary file that fails at ONE step, and counts nothing
// else: Close always releases the descriptor, as (*os.File).Close does.
type failingTemp struct {
	f    *os.File
	fail string
}

func (w *failingTemp) Write(p []byte) (int, error) {
	if w.fail == "write" {
		return 0, errors.New("the disk said no to the write")
	}
	return w.f.Write(p)
}

func (w *failingTemp) Close() error {
	err := w.f.Close()
	if w.fail == "close" {
		return errors.New("the disk said no to the close")
	}
	return err
}

func (w *failingTemp) Name() string { return w.f.Name() }

// record.go: a write or a close that fails leaves no record and no stray
// temporary, and says why.
func TestARecordWhoseWriteOrCloseFailsLeavesNothingBehind(t *testing.T) {
	for _, fail := range []string{"write", "close"} {
		t.Run(fail, func(t *testing.T) {
			store := t.TempDir()
			t.Setenv(recordsEnv, store)
			real := createRecordTemp
			t.Cleanup(func() { createRecordTemp = real })
			createRecordTemp = func(dir, pattern string) (recordTemp, error) {
				f, err := os.CreateTemp(dir, pattern)
				if err != nil {
					t.Fatal(err)
				}
				return &failingTemp{f: f, fail: fail}, nil
			}
			err := storeRecord(record{SHA256: "k", At: time.Now()})
			if err == nil || !strings.Contains(err.Error(), "the disk said no to the "+fail) {
				t.Fatalf("error = %v", err)
			}
			entries, rerr := os.ReadDir(store)
			if rerr != nil {
				t.Fatal(rerr)
			}
			if len(entries) != 0 {
				t.Errorf("the store holds %d entries after a failed %s", len(entries), fail)
			}
		})
	}
}

// record.go: a sweep in which mutate never started files nothing.
func TestNothingIsFiledForASweepThatNeverRan(t *testing.T) {
	store := t.TempDir()
	t.Setenv(recordsEnv, store)
	dir := t.TempDir()
	write(t, dir, "x.go", oneGuard)
	g := oneGuardOf(t, dir)[0]
	err := writeRecords(config{dir: dir}, []result{{guard: g, verdict: caught}, {guard: g, verdict: unrun}}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "could not be started") {
		t.Fatalf("error = %v", err)
	}
	if entries, _ := os.ReadDir(store); len(entries) != 0 {
		t.Errorf("a record was filed for a sweep that did not run: %d entries", len(entries))
	}
}

// record.go: a swept file that vanished before its record was filed.
func TestAFileThatVanishedAfterTheSweepIsAnError(t *testing.T) {
	t.Setenv(recordsEnv, t.TempDir())
	dir := t.TempDir()
	g := guard{dir: dir, file: "gone.go", line: 3}
	if err := writeRecords(config{dir: dir}, []result{{guard: g, verdict: caught}}, &bytes.Buffer{}); err == nil {
		t.Fatal("a record was filed for a file that is not there")
	}
}

// record.go: an -only run keeps the verdicts of the lines it did not ask about,
// adds the ones it did, and says when it cannot file.
func TestAnOnlyRunKeepsWhatItDidNotAsk(t *testing.T) {
	store := t.TempDir()
	t.Setenv(recordsEnv, store)
	dir := t.TempDir()
	write(t, dir, "x.go", oneGuard)
	src, err := os.ReadFile(filepath.Join(dir, "x.go"))
	if err != nil {
		t.Fatal(err)
	}
	key := contentKey(src)
	if err := storeRecord(record{SHA256: key, File: "x.go", Guards: 9, Lines: []int{5},
		Needed: []entry{{Line: 5, Verdict: survived, Cond: "old"}, {Line: 9, Verdict: hung, Cond: "stale"}}}); err != nil {
		t.Fatal(err)
	}
	asked := guard{dir: dir, file: "x.go", line: 9, cond: "new"}
	cfg := config{dir: dir, only: []target{{file: "x.go", line: 9}}}
	if err := writeRecords(cfg, []result{{guard: asked, verdict: survived}}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := lookupRecord(key)
	if err != nil || !ok {
		t.Fatalf("lookup: %v %v", ok, err)
	}
	if len(rec.Needed) != 2 || rec.Needed[0] != (entry{Line: 5, Verdict: survived, Cond: "old"}) ||
		rec.Needed[1] != (entry{Line: 9, Verdict: survived, Cond: "new"}) {
		t.Errorf("needed = %+v, want line 5 kept and line 9 replaced", rec.Needed)
	}
	if len(rec.Lines) != 2 || rec.Lines[0] != 5 || rec.Lines[1] != 9 {
		t.Errorf("lines = %v, want [5 9]", rec.Lines)
	}

	// And a store that will not take the record is an error, not a silent pass.
	real := createRecordTemp
	t.Cleanup(func() { createRecordTemp = real })
	createRecordTemp = func(string, string) (recordTemp, error) { return nil, errors.New("read-only store") }
	if err := writeRecords(cfg, []result{{guard: asked, verdict: caught}}, &bytes.Buffer{}); err == nil ||
		!strings.Contains(err.Error(), "read-only store") {
		t.Errorf("error = %v", err)
	}
}

// ---- sweep.go ------------------------------------------------------------

const heldOne = `package x

import "testing"

func TestOne(t *testing.T) {
	if One(-1) == nil {
		t.Fatal("a negative was accepted")
	}
}
`

// sweep.go: a command that asks for JSON is read as JSON, and the sweep says so.
func TestASweepSaysWhenItReadsJSON(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module x\n\ngo 1.27.1\n")
	write(t, dir, "x.go", oneGuard)
	write(t, dir, "x_test.go", heldOne)
	cfg := config{dir: dir, runIn: dir, timeout: 2 * time.Minute, mutate: buildMutate(t)}
	var out, errOut bytes.Buffer
	if code := sweep(cfg, oneGuardOf(t, dir), []string{"go", "test", "-json", "-count=1", "./..."}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d:\n%s\n%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "read from "+string(byJSON)) {
		t.Errorf("the sweep does not say it read JSON:\n%s", out.String())
	}
}

func TestAMutateThatCannotBeRunIsRefusedUpFront(t *testing.T) {
	var errOut bytes.Buffer
	code := sweep(config{dir: t.TempDir(), timeout: time.Second, mutate: filepath.Join(t.TempDir(), "absent")},
		[]guard{{}}, []string{"true"}, &bytes.Buffer{}, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "could not run") {
		t.Errorf("exit %d: %q", code, errOut.String())
	}
}

// sweep.go: a config with no directory at all runs nothing, anywhere.
func TestNoDirectoryMeansNothingRunsAnywhere(t *testing.T) {
	var errOut bytes.Buffer
	if _, code := baseline(config{timeout: time.Second}, []string{"true"}, &errOut); code != 2 ||
		!strings.Contains(errOut.String(), "no directory") {
		t.Errorf("baseline: exit %d: %q", code, errOut.String())
	}
	got := oneMutation(config{timeout: time.Second}, guard{}, []string{"true"})
	if got.verdict != noBuild || got.note != "no directory to run in" {
		t.Errorf("oneMutation = %q (%s)", got.verdict, got.note)
	}
}

// stubMutate builds a mutate that answers -h with -echo and otherwise prints
// $STUB_OUT and exits with $STUB_EXIT: every shape of answer a real one gives,
// without running a suite.
func stubMutate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "go.mod", "module stub\n\ngo 1.27.1\n")
	write(t, dir, "main.go", `package main

import (
	"fmt"
	"os"
	"strconv"
)

func main() {
	for _, a := range os.Args[1:] {
		if a == "-h" {
			fmt.Println("-echo")
			return
		}
	}
	fmt.Print(os.Getenv("STUB_OUT"))
	n, _ := strconv.Atoi(os.Getenv("STUB_EXIT"))
	os.Exit(n)
}
`)
	bin := filepath.Join(t.TempDir(), "mutate"+exeSuffix())
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the stub: %v\n%s", err, out)
	}
	return bin
}

// sweep.go: a mutate that exits with a status nobody defined is "not a mutant",
// with its last line as the note.
func TestAnUnknownExitStatusIsNotAMutantAndSaysWhatItSaid(t *testing.T) {
	bin := stubMutate(t)
	t.Setenv("STUB_EXIT", "7") // 3 and 4 are mutate's "the machine failed" (environ.go)
	t.Setenv("STUB_OUT", "something unexpected\n")
	dir := t.TempDir()
	write(t, dir, "x.go", oneGuard)
	got := oneMutation(config{dir: dir, timeout: time.Minute, mutate: bin}, oneGuardOf(t, dir)[0], []string{"true"})
	if got.verdict != noBuild || got.note != "something unexpected" {
		t.Errorf("verdict %q, note %q", got.verdict, got.note)
	}
}

// sweep.go: when neither the deletion nor `false && (C)` compiles, the note says
// both were tried.
func TestWhenNeitherMutationCompilesTheNoteSaysBothWereTried(t *testing.T) {
	bin := stubMutate(t)
	t.Setenv("STUB_EXIT", "2")
	t.Setenv("STUB_OUT", "DID NOT COMPILE\nx.go:3:1: broken\n")
	dir := t.TempDir()
	write(t, dir, "x.go", oneGuard)
	g := oneGuardOf(t, dir)[0]
	g.altFrom, g.altTo = "alt-from", "alt-to"
	got := one(config{dir: dir, timeout: time.Minute, mutate: bin}, g, []string{"true"})
	if got.verdict != noBuild || !strings.HasPrefix(got.note, "neither the deletion nor false && (C) compiles: ") {
		t.Errorf("verdict %q, note %q", got.verdict, got.note)
	}
	if got.guard.altFrom != "alt-from" {
		t.Errorf("the result names the retry's guard, not the one swept: %+v", got.guard)
	}
}

func TestFirstBuildErrorFallsBackToASentence(t *testing.T) {
	if got := firstBuildError("DID NOT COMPILE\nnothing with a position\n"); got != "did not compile" {
		t.Errorf("got %q", got)
	}
	if got := firstBuildError("noise\nx.go:3:1: broken\n"); got != "x.go:3:1: broken" {
		t.Errorf("got %q", got)
	}
}

func TestTrimBoundsAFieldToRunes(t *testing.T) {
	for _, tt := range []struct {
		s    string
		n    int
		want string
	}{
		{"abc", 0, ""},
		{"abc", -1, ""},
		{"abc", 1, "…"},
		{"abc", 2, "a…"},
		{"abc", 3, "abc"},
		{"abcd", 3, "ab…"},
		{"⛔⛔⛔⛔", 3, "⛔⛔…"},
		{"", 5, ""},
		// A byte that is not UTF-8 is one rune of width 1, as ranging says.
		{"a\xffbcd", 3, "a\xff…"},
	} {
		if got := trim(tt.s, tt.n); got != tt.want {
			t.Errorf("trim(%q, %d) = %q, want %q", tt.s, tt.n, got, tt.want)
		}
	}
}

// sweep.go: the tail of a long output starts on a rune boundary.
func TestLastOfDropsAHalfRuneAtTheCut(t *testing.T) {
	// 702 bytes: the last 600 begin in the MIDDLE of the 51st é.
	in := []byte("a" + strings.Repeat("é", 350) + "b")
	got := lastOf(in)
	if !utf8.ValidString(got) {
		t.Fatalf("lastOf is not valid UTF-8: %q", got[:8])
	}
	if !strings.HasPrefix(got, "…é") || !strings.HasSuffix(got, "éb") {
		t.Errorf("lastOf = %q…%q", got[:8], got[len(got)-4:])
	}
	// 299 whole é and the final b: the half rune at the cut is gone.
	if n := len(got) - len("…"); n != 299*2+1 {
		t.Errorf("lastOf kept %d bytes after the ellipsis", n)
	}
}

// sweep.go: the restore of last resort says why it could not restore.
func TestEnsureRestoredSaysWhyItCouldNot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.go")
	if err := ensureRestored(path, nil, errors.New("never read")); err == nil || !strings.Contains(err.Error(), "never read") {
		t.Errorf("a file that was never read: %v", err)
	}
	if err := ensureRestored(path, []byte("x"), nil); err == nil || !strings.Contains(err.Error(), "cannot read") {
		t.Errorf("a file that is gone: %v", err)
	}
	if err := os.WriteFile(path, []byte("same"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureRestored(path, []byte("same"), nil); err != nil {
		t.Errorf("a file already as it was: %v", err)
	}
	// A file that differs is put back, and the caller is TOLD it had to be.
	if err := ensureRestored(path, []byte("before"), nil); err == nil || !strings.Contains(err.Error(), "put back") {
		t.Fatalf("a file that differs: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "before" {
		t.Errorf("file = %q, want it restored", got)
	}
}
