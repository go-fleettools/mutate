package main

import (
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
			guards, err := collect(config{dir: dir})
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

	gs, err := collect(config{dir: dir})
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
