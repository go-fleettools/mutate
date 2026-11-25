package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const hangSrc = `package x

func Wait(n int) {
	if n <= 0 {
		return
	}
	select {}
}
`

// noTemps fails if any `.*.atomicwrite-*` file is left under dir.
func noTemps(t *testing.T, dir string) {
	t.Helper()
	left, err := staleTemps(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("temp file(s) left in the swept tree: %v", left)
	}
}

func hangFixture(t *testing.T) (dir string, g guard) {
	t.Helper()
	dir = t.TempDir()
	write(t, dir, "go.mod", "module x\n\ngo 1.27.1\n")
	write(t, dir, "x.go", hangSrc)
	write(t, dir, "x_test.go", "package x\n\nimport \"testing\"\n\nfunc TestReturns(t *testing.T) { Wait(0) }\n")
	gs, _, err := collect(config{dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	return dir, gs[0]
}

func intact(t *testing.T, dir string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "x.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != hangSrc {
		t.Fatalf("original not byte-identical:\n%s", b)
	}
}

// A HUNG mutant, killed by the timeout, leaves no temp file and the original intact.
func TestAHungMutantLeavesNoTempFile(t *testing.T) {
	dir, g := hangFixture(t)
	got := one(config{dir: dir, timeout: 3 * time.Second, mutate: buildMutate(t)}, g, []string{"go", "test", "./..."})
	if got.verdict != hung {
		t.Fatalf("verdict %q (%s), want %q", got.verdict, got.note, hung)
	}
	noTemps(t, dir)
	intact(t, dir)
}

// A mutate that is killed mid-mutation and cannot restore (the sweep's own
// last-resort restore runs) leaves no temp file and the original intact.
func TestAKilledMutationLeavesNoTempFile(t *testing.T) {
	dir, g := hangFixture(t)
	got := one(config{dir: dir, timeout: 2 * time.Second, mutate: buildStubborn(t)}, g, []string{"go", "test", "./..."})
	if got.verdict != hung {
		t.Fatalf("verdict %q (%s), want %q", got.verdict, got.note, hung)
	}
	noTemps(t, dir)
	intact(t, dir)
}

// The error path: mutate cannot even be started.
func TestAnErrorPathLeavesNoTempFile(t *testing.T) {
	dir, g := hangFixture(t)
	got := one(config{dir: dir, timeout: 30 * time.Second, mutate: filepath.Join(dir, "no-such-mutate")}, g,
		[]string{"go", "test", "./..."})
	if got.verdict != unrun {
		t.Fatalf("verdict %q (%s), want %q", got.verdict, got.note, unrun)
	}
	noTemps(t, dir)
	intact(t, dir)
}

// The gate: a stale temp file, in dir or in the run-in tree, is refused before
// anything is mutated, and every one is named.
func TestASweepRefusesAStaleAtomicwriteTemp(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x.go", oneGuard)
	mutateBin := buildMutate(t)
	cfg := config{dir: dir, timeout: 30 * time.Second, mutate: mutateBin}

	var out, errOut bytes.Buffer
	if code := sweep(cfg, oneGuardOf(t, dir), []string{"true"}, &out, &errOut); code == 2 {
		t.Fatalf("the control was refused: %s", errOut.String())
	}

	root := t.TempDir()
	sub := filepath.Join(root, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, sub, "x.go", oneGuard)
	write(t, sub, ".x.go.atomicwrite-123", "stale")
	write(t, root, ".kdf.go.atomicwrite-4168939430", "stale")
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, gitDir, ".y.go.atomicwrite-9", "inside .git is not the tree")

	cfg = config{dir: sub, runIn: root, timeout: 30 * time.Second, mutate: mutateBin}
	out.Reset()
	errOut.Reset()
	code := sweep(cfg, oneGuardOf(t, sub), []string{"true"}, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	for _, want := range []string{filepath.Join(sub, ".x.go.atomicwrite-123"),
		filepath.Join(root, ".kdf.go.atomicwrite-4168939430"), "2 stale"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("the refusal does not mention %q: %s", want, errOut.String())
		}
	}
	if strings.Contains(errOut.String(), ".y.go") {
		t.Errorf("a file under .git was named: %s", errOut.String())
	}
	if out.Len() != 0 {
		t.Errorf("it started anyway: %s", out.String())
	}
	intactSrc, _ := os.ReadFile(filepath.Join(sub, "x.go"))
	if string(intactSrc) != oneGuard {
		t.Errorf("the source was touched")
	}
}

func TestAnUnreadableTreeIsRefusedNotSkipped(t *testing.T) {
	cfg := config{dir: filepath.Join(t.TempDir(), "absent")}
	err := refuseStale(cfg)
	if err == nil || !strings.Contains(err.Error(), "looking for stale") {
		t.Fatalf("err = %v, want a refusal that says it could not look", err)
	}
}

func TestWhatCountsAsAnAtomicwriteTemp(t *testing.T) {
	for name, want := range map[string]bool{
		".kdf.go.atomicwrite-4168939430": true,
		".a.atomicwrite-1":               true,
		"kdf.go.atomicwrite-1":           false,
		".atomicwrite-1":                 false,
		".kdf.go.atomicwrite-":           false,
		".kdf.go":                        false,
	} {
		if got := isAtomicTemp(name); got != want {
			t.Errorf("isAtomicTemp(%q) = %v, want %v", name, got, want)
		}
	}
}
