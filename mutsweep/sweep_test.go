package main

import (
	"os/exec"
	"path/filepath"
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

// buildMutate compiles the command at the repository root and returns its path.
func buildMutate(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "mutate")
	cmd := exec.Command("go", "build", "-o", bin, "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building mutate from this repository: %v\n%s", err, out)
	}
	return bin
}
