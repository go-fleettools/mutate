package main

import (
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
			if got := report(tt.in, &out, &errOut); got != tt.want {
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
	if code := report([]result{{verdict: caught}, got}, &out, &errOut); code != 1 {
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
