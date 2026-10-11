package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Each verdict, end to end, against a real `go test` and the real mutate.
//
// A skipped test is not a passing one, so a missing mutate fails this rather
// than quietly removing the only coverage these four lines have.
func TestEveryVerdict(t *testing.T) {
	// The mutate THIS repository holds, built here: a sweep is only as good as
	// the mutation discipline underneath it, and testing against whatever
	// happens to be installed would test somebody else's.
	mutateBin := buildMutate(t)

	for _, tt := range []struct {
		name    string
		source  string
		test    string
		want    verdict
		timeout time.Duration
	}{
		{
			name: "caught: a test asserts the refusal",
			source: `package x

import "errors"

var ErrNegative = errors.New("negative")

func Check(n int) error {
	if n < 0 {
		return ErrNegative
	}
	return nil
}
`,
			test: `package x

import "testing"

func TestRefuses(t *testing.T) {
	if Check(-1) == nil {
		t.Fatal("a negative was accepted")
	}
}
`,
			want: caught,
		},
		{
			name: "survived: nothing asks",
			// The error is a package-level var, so deleting the guard leaves
			// no orphan and the mutant COMPILES. An earlier version of this
			// fixture called errors.New inline, the import fell unused, and
			// the tool rightly called it "not a mutant" -- which is the trap
			// it exists to name, met here by its own test.
			source: `package x

import "errors"

var errNegative = errors.New("negative")

func Check(n int) error {
	if n < 0 {
		return errNegative
	}
	return nil
}
`,
			test: `package x

import "testing"

func TestOnlyTheHappyPath(t *testing.T) {
	if Check(1) != nil {
		t.Fatal("a positive was refused")
	}
}
`,
			want: survived,
		},
		{
			name: "not a mutant: deleting it orphans a variable",
			source: `package x

import "errors"

func Check(n int) (int, error) {
	doubled := n * 2
	if n < 0 {
		return doubled, errors.New("negative")
	}
	return 0, nil
}
`,
			test: `package x

import "testing"

func TestHappy(t *testing.T) {
	if _, err := Check(1); err != nil {
		t.Fatal(err)
	}
}
`,
			want: noBuild,
		},
		{
			name: "hung: without it the test never returns",
			source: `package x

func Wait(n int) {
	if n <= 0 {
		return
	}
	select {}
}
`,
			test: `package x

import "testing"

func TestReturns(t *testing.T) { Wait(0) }
`,
			want:    hung,
			timeout: 3 * time.Second,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, "go.mod", "module x\n\ngo 1.27.1\n")
			write(t, dir, "x.go", tt.source)
			write(t, dir, "x_test.go", tt.test)

			timeout := tt.timeout
			if timeout == 0 {
				timeout = 2 * time.Minute
			}
			guards, _, err := collect(config{dir: dir})
			if err != nil {
				t.Fatal(err)
			}
			if len(guards) != 1 {
				t.Fatalf("expected one guard to delete, found %d", len(guards))
			}
			got := one(config{dir: dir, timeout: timeout, mutate: mutateBin}, guards[0],
				[]string{"go", "test", "-count", "1", "./..."})
			if got.verdict != tt.want {
				t.Fatalf("verdict %q (%s), want %q", got.verdict, got.note, tt.want)
			}
		})
	}
}

// The summary exits non-zero exactly when something needs reading.
func TestReportExitsOnWhatNeedsReading(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   []result
		want int
	}{
		{"all caught", []result{{verdict: caught}, {verdict: caught}}, 0},
		{"not mutants are not failures", []result{{verdict: noBuild}, {verdict: caught}}, 0},
		{"one survivor", []result{{verdict: caught}, {verdict: survived}}, 1},
		{"one hang", []result{{verdict: caught}, {verdict: hung}}, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut strings.Builder
			if got := report(tt.in, time.Minute, &out, &errOut); got != tt.want {
				t.Fatalf("exit %d, want %d", got, tt.want)
			}
		})
	}
}

// exeSuffix is what this platform needs on an executable it will run: Windows
// will not start a file without it, and `go build -o` does not add one.
func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// buildMutate compiles the command at the repository root and returns its path.
func buildMutate(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "mutate"+exeSuffix())
	cmd := exec.Command("go", "build", "-o", bin, "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building mutate from this repository: %v\n%s", err, out)
	}
	return bin
}

// "mutate could not be started" is a statement about this machine, not about
// the code under test. Windows CI said it first, and said it as "not a mutant"
// with an empty note: the binary had been built without the extension Windows
// needs, so nothing ran and every guard read as unmutable.
func TestAMutateThatCannotRunIsNotAVerdictAboutTheCode(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module x\n\ngo 1.27.1\n")
	write(t, dir, "x.go", "package x\n\nimport \"errors\"\n\nvar e = errors.New(\"no\")\n\nfunc F(n int) error {\n\tif n < 0 {\n\t\treturn e\n\t}\n\treturn nil\n}\n")
	write(t, dir, "x_test.go", "package x\n\nimport \"testing\"\n\nfunc TestF(t *testing.T) {\n\tif F(1) != nil {\n\t\tt.Fatal(\"refused a positive\")\n\t}\n}\n")

	gs, _, err := collect(config{dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := one(config{dir: dir, timeout: 30 * time.Second, mutate: filepath.Join(dir, "no-such-mutate")},
		gs[0], []string{"go", "test", "./..."})
	if got.verdict != unrun {
		t.Fatalf("verdict %q (%s), want %q", got.verdict, got.note, unrun)
	}
	if !strings.Contains(got.note, "could not run") {
		t.Errorf("the note does not say what happened: %q", got.note)
	}

	// And a run that could not run fails the summary, rather than passing as a
	// sweep in which nothing survived.
	var out, errOut strings.Builder
	if code := report([]result{{verdict: caught}, got}, time.Minute, &out, &errOut); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "never ran") {
		t.Errorf("the summary does not say they never ran: %q", errOut.String())
	}
}

// A sweep that gives up on a mutant must leave the source as it found it.
//
// mutate restores the file when it is interrupted, so the sweep asks before it
// kills -- and checks afterwards, because depending on another process having
// managed that is a hope. Measured before this existed: after a HUNG verdict
// the file still held the deliberate defect, which is the exact failure mutate
// was written to prevent, reintroduced by the thing driving it.
func TestTheFileComesBackAfterAHang(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module x\n\ngo 1.27.1\n")
	const src = `package x

func Wait(n int) {
	if n <= 0 {
		return
	}
	select {}
}
`
	write(t, dir, "x.go", src)
	write(t, dir, "x_test.go", "package x\n\nimport \"testing\"\n\nfunc TestReturns(t *testing.T) { Wait(0) }\n")

	gs, _, err := collect(config{dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := one(config{dir: dir, timeout: 3 * time.Second, mutate: buildMutate(t)}, gs[0],
		[]string{"go", "test", "./..."})
	if got.verdict != hung {
		t.Fatalf("verdict %q (%s), want %q -- this test is about what happens after a hang",
			got.verdict, got.note, hung)
	}
	after, err := os.ReadFile(filepath.Join(dir, "x.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != src {
		t.Fatalf("the file was left mutated after the sweep gave up:\n%s", after)
	}
}

// The restore must not depend on the mutating process cleaning up after
// itself, and must not be done by matching text.
//
// Both were learned on Windows, where taskkill without /F does not stop a
// console program: mutate never restored, the sweep's own restore ran for the
// first time -- and corrupted the file, because a uniquely spelled guard is
// replaced by the EMPTY string, and putting it back by matching that inserts it
// at offset zero. The file came back as "if n <= 0 {...}" followed by
// "package x".
//
// This reproduces that on any platform with a stand-in for mutate that applies
// the replacement, ignores being asked to stop, and sleeps.
func TestTheFileComesBackEvenIfNothingElseRestoresIt(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module x\n\ngo 1.27.1\n")
	const src = `package x

func Wait(n int) {
	if n <= 0 {
		return
	}
	select {}
}
`
	write(t, dir, "x.go", src)
	write(t, dir, "x_test.go", "package x\n\nimport \"testing\"\n\nfunc TestReturns(t *testing.T) { Wait(0) }\n")

	gs, _, err := collect(config{dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := one(config{dir: dir, timeout: 2 * time.Second, mutate: buildStubborn(t)}, gs[0],
		[]string{"go", "test", "./..."})
	if got.verdict != hung {
		t.Fatalf("verdict %q (%s), want %q", got.verdict, got.note, hung)
	}
	if !strings.Contains(got.note, "put back here") {
		t.Errorf("the note does not say the sweep restored it: %q", got.note)
	}
	after, err := os.ReadFile(filepath.Join(dir, "x.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != src {
		t.Fatalf("the file did not come back as it was:\n%s", after)
	}
}

// buildStubborn compiles a stand-in for mutate that applies the replacement,
// refuses to be asked to stop, and then sleeps.
func buildStubborn(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	const prog = `package main

import (
	"flag"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	file := flag.String("file", "", "")
	from := flag.String("from", "", "")
	to := flag.String("to", "", "")
	flag.String("name", "", "")
	flag.Parse()
	b, err := os.ReadFile(*file)
	if err != nil {
		os.Exit(2)
	}
	_ = os.WriteFile(*file, []byte(strings.Replace(string(b), *from, *to, 1)), 0o644)
	signal.Ignore(syscall.SIGTERM, syscall.SIGINT)
	time.Sleep(time.Hour)
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(prog), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module stubborn\n\ngo 1.27.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "stubborn"+exeSuffix())
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the stand-in: %v\n%s", err, out)
	}
	return bin
}

// A sweep over an already-red suite would report every guard as caught.
//
// This is the worst answer this tool can give, because it is the most
// reassuring: the command fails whatever is deleted, so every mutant looks
// held and the summary reads "0 survived". It ran without this check until
// 2026-10-11.
//
// The control comes first and it is the same fixture with a passing command, so
// what follows is about the suite being red and not about the fixture.
func TestASweepRefusesASuiteThatIsAlreadyRed(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x.go", oneGuard)
	cfg := config{dir: dir, timeout: 30 * time.Second, mutate: buildMutate(t)}

	var out, errOut bytes.Buffer
	if code := sweep(cfg, oneGuardOf(t, dir), []string{"true"}, &out, &errOut); code == 2 {
		t.Fatalf("the control was refused: %s", errOut.String())
	}

	out.Reset()
	errOut.Reset()
	code := sweep(cfg, oneGuardOf(t, dir), []string{"false"}, &out, &errOut)
	if code != 2 {
		t.Errorf("exit %d, want 2 — a red suite reports every guard as caught", code)
	}
	if !strings.Contains(errOut.String(), "FAILS with nothing mutated") {
		t.Errorf("the refusal does not say why: %s", errOut.String())
	}
	if strings.Contains(out.String(), "caught") {
		t.Errorf("it swept anyway and reported a verdict: %s", out.String())
	}
}

// A timeout that is not comfortably above the suite's own duration makes HUNG
// meaningless, so it is refused with both numbers.
//
// Measured on go-crdt/collab: a 60-second suite under a 5-minute timeout
// produced six HUNG, at least two of which were caught in seconds by their own
// targeted test. A legitimate survivor in the same run took 4m22s.
func TestASweepRefusesATimeoutTooCloseToTheSuitesOwnDuration(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x.go", oneGuard)
	mutateBin := buildMutate(t)

	// A command that takes a measurable moment, so the margin is a real ratio
	// rather than a comparison against zero.
	slow := []string{"sleep", "1"}

	var out, errOut bytes.Buffer
	roomy := config{dir: dir, timeout: 20 * time.Second, mutate: mutateBin}
	if code := sweep(roomy, oneGuardOf(t, dir), slow, &out, &errOut); code == 2 {
		t.Fatalf("the control was refused with twenty times the margin: %s", errOut.String())
	}
	if !strings.Contains(out.String(), "the suite itself takes") {
		t.Errorf("the baseline is not reported: %s", out.String())
	}

	errOut.Reset()
	thin := config{dir: dir, timeout: 2 * time.Second, mutate: mutateBin}
	if code := sweep(thin, oneGuardOf(t, dir), slow, &bytes.Buffer{}, &errOut); code != 2 {
		t.Errorf("exit %d, want 2 — two seconds for a one-second suite cannot tell slow from hung", code)
	}
	for _, want := range []string{"the suite takes", "-timeout"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("the refusal does not mention %q: %s", want, errOut.String())
		}
	}
}

// And a command that does not finish at all with nothing mutated is refused
// before any guard is touched.
func TestASweepRefusesACommandThatCannotEvenFinishUnmutated(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x.go", oneGuard)
	cfg := config{dir: dir, timeout: time.Second, mutate: buildMutate(t)}

	var errOut bytes.Buffer
	code := sweep(cfg, oneGuardOf(t, dir), []string{"sleep", "30"}, &bytes.Buffer{}, &errOut)
	if code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "NOTHING mutated") {
		t.Errorf("the refusal does not say so: %s", errOut.String())
	}
}

const oneGuard = `package x

import "errors"

func One(n int) error {
	if n < 0 {
		return errors.New("negative")
	}
	return nil
}
`

func oneGuardOf(t *testing.T, dir string) []guard {
	t.Helper()
	gs, _, err := collect(config{dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(gs) != 1 {
		t.Fatalf("the fixture holds %d guards, want 1", len(gs))
	}
	return gs
}

// A HUNG verdict sitting beside a mutant that ANSWERED slowly is suspect, and
// the report says so.
//
// This is the signal the baseline margin cannot give. On go-crdt/collab the
// suite took 60s under a 5m timeout -- five times over, which
// marginOverBaseline allows -- and six mutants came back HUNG while a
// legitimate SURVIVOR in the same run took 4m22s. Two of the six were then
// caught in seconds by their own targeted test. The ratio that mattered was
// timeout over the slowest mutant that FINISHED, not over the baseline.
func TestAHungVerdictBesideASlowAnswerIsFlagged(t *testing.T) {
	timeout := time.Minute

	// The control: a hang beside answers that finished quickly says nothing
	// about the timeout, so there must be no caution.
	var out, errOut strings.Builder
	report([]result{
		{verdict: caught, took: time.Second},
		{verdict: hung},
	}, timeout, &out, &errOut)
	// "slower rather than stuck", not "may be": the closing paragraph below
	// already says "it may be a bound doubled one layer down", so a witness
	// matching that would fire on the help text instead of on the caution.
	if strings.Contains(errOut.String(), "slower rather than stuck") {
		t.Errorf("a quick run was flagged: %s", errOut.String())
	}

	// And the collab shape: an answer at 44s of 60s allowed.
	out.Reset()
	errOut.Reset()
	report([]result{
		{verdict: survived, took: 44 * time.Second, guard: guard{file: "a.go", line: 1}},
		{verdict: hung, guard: guard{file: "b.go", line: 2}},
		{verdict: hung, guard: guard{file: "c.go", line: 3}},
	}, timeout, &out, &errOut)
	got := errOut.String()
	for _, want := range []string{"slower rather than stuck", "44s of the 1m0s allowed", "2 HUNG", "-only b.go:2,c.go:3"} {
		if !strings.Contains(got, want) {
			t.Errorf("the caution does not contain %q:\n%s", want, got)
		}
	}
}

// nearTheLimit ignores the hung ones when looking for the slowest answer: a
// hang's elapsed time IS the timeout, so counting it would flag every sweep
// that had one.
func TestTheSlowestAnswerIgnoresTheHangs(t *testing.T) {
	rs := []result{
		{verdict: hung, took: time.Minute},
		{verdict: caught, took: 2 * time.Second},
	}
	slowest, close := nearTheLimit(rs, time.Minute)
	if slowest != 2*time.Second {
		t.Errorf("slowest answer %v, want 2s — a hang's own duration is the timeout", slowest)
	}
	if close {
		t.Error("two seconds of a minute was called close")
	}
}
