package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ⛔ A FULL DISK IS NOT "NOT A MUTANT". See environ.go for the 2026-11-27 account.

func TestEnvironmentCauseReadsTheToolchainAndNotTheTests(t *testing.T) {
	for _, tt := range []struct {
		name, out string
		want      bool
	}{
		{"compiler", "# x\ncompile: writing output: write $WORK/b001/_pkg_.a: no space left on device\nFAIL x [build failed]\n", true},
		{"go command", "go: writing go.mod: open /tmp/go.mod: No space left on device\n", true},
		{"mutate itself", "mutate: atomicwrite g.go: open ./.g.go.atomicwrite-1: disk quota exceeded\n", true},
		{"read-only", "mutate: ENVIRONMENT FAILURE: Read-only file system\n", true},
		{"json build output", `{"Action":"build-output","Output":"compile: no space left on device\n"}` + "\n", true},
		{"a test's own words, indented", "--- FAIL: TestFull (0.00s)\n    full_test.go:9: got no space left on device\nFAIL\n", false},
		{"a test's own words, in json", `{"Action":"output","Test":"TestFull","Output":"no space left on device\n"}` + "\n", false},
		{"nothing", "ok  \tx\t0.1s\n", false},
	} {
		if got := environmentCause(tt.out) != ""; got != tt.want {
			t.Errorf("%s: environmentCause says %v, want %v", tt.name, got, tt.want)
		}
	}
}

// A stand-in mutate that fails the way a full disk makes it fail. Mode comes from
// STANDIN_MODE, every call is counted in STANDIN_COUNT, and it applies the
// replacement before it fails when it has to leave the file mutated.
func buildStandIn(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	const prog = `package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	file := flag.String("file", "", "")
	from := flag.String("from", "", "")
	to := flag.String("to", "", "")
	flag.String("name", "", "")
	flag.Bool("echo", false, "")
	flag.Parse()
	if c := os.Getenv("STANDIN_COUNT"); c != "" {
		f, _ := os.OpenFile(c, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		fmt.Fprintln(f, *file)
		f.Close()
	}
	switch os.Getenv("STANDIN_MODE") {
	case "compile-enospc":
		fmt.Fprintln(os.Stderr, "mutate: g DID NOT COMPILE — rewrite the mutation, this proves nothing")
		fmt.Fprintln(os.Stderr, "    # x")
		fmt.Fprintln(os.Stderr, "compile: writing output: write $WORK/b001/_pkg_.a: no space left on device")
		os.Exit(2)
	case "atomicwrite":
		fmt.Fprintln(os.Stderr, "mutate: atomicwrite x.go: open ./.x.go.atomicwrite-123: no space left on device")
		os.Exit(2)
	case "apply-fail":
		fmt.Fprintln(os.Stderr, "mutate: ENVIRONMENT FAILURE, the mutation was not applied: write x.go: input/output error")
		os.Exit(3)
	case "restore-fail", "restore-fail-repairable":
		b, _ := os.ReadFile(*file)
		_ = os.WriteFile(*file, []byte(strings.Replace(string(b), *from, *to, 1)), 0o644)
		fmt.Fprintln(os.Stderr, "mutate: COULD NOT RESTORE "+*file+": no space left on device")
		if os.Getenv("STANDIN_MODE") == "restore-fail" {
			_ = os.Chmod(*file, 0o444)
			_ = os.Chmod(".", 0o500) // nothing can put it back
		}
		os.Exit(4)
	case "leaves-mutated":
		// Exits "caught" and leaves the bytes changed, in a directory that can be written.
		b, _ := os.ReadFile(*file)
		_ = os.WriteFile(*file, []byte(strings.Replace(string(b), *from, *to, 1)), 0o644)
		os.Exit(0)
	case "walks-away":
		// Exits "caught" and leaves the file mutated: nothing says so but the bytes.
		b, _ := os.ReadFile(*file)
		_ = os.WriteFile(*file, []byte(strings.Replace(string(b), *from, *to, 1)), 0o444)
		_ = os.Chmod(*file, 0o444)
		_ = os.Chmod(".", 0o500) // mutate runs in the directory of the file
		os.Exit(0)
	}
	fmt.Println("mutate: caught")
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(prog), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module standin\n\ngo 1.27.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "standin"+exeSuffix())
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the stand-in: %v\n%s", err, out)
	}
	return bin
}

const twoGuards = `package x

import "errors"

var e = errors.New("no")

func A(n int) error {
	if n < 0 {
		return e
	}
	return nil
}

func B(n int) error {
	if n > 100 {
		return e
	}
	return nil
}
`

// fixture is a module with two guards, and the counter file the stand-in writes to.
func fixture(t *testing.T, mode string) (cfg config, count string) {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "go.mod", "module x\n\ngo 1.27.1\n")
	write(t, dir, "x.go", twoGuards)
	count = filepath.Join(t.TempDir(), "count")
	t.Setenv("STANDIN_MODE", mode)
	t.Setenv("STANDIN_COUNT", count)
	return config{dir: dir, runIn: dir, timeout: time.Minute, mutate: buildStandIn(t)}, count
}

func calls(t *testing.T, count string) int {
	t.Helper()
	b, err := os.ReadFile(count)
	if err != nil {
		return 0
	}
	return len(strings.Fields(string(b)))
}

func sweepAll(t *testing.T, cfg config) (code int, stdout, stderr string) {
	t.Helper()
	gs, _, err := collect(cfg)
	if err != nil || len(gs) != 2 {
		t.Fatalf("collect: %d guards, %v", len(gs), err)
	}
	var out, errb strings.Builder
	code = sweep(cfg, gs, []string{"go", "version"}, &out, &errb)
	return code, out.String(), errb.String()
}

// 1: an I/O failure, or a build that failed because of the environment, is never
// "not a mutant": the sweep stops after the FIRST one, exits non-zero and names it.
func TestAnEnvironmentFailureStopsTheSweepAndIsNeverNotAMutant(t *testing.T) {
	for _, tt := range []struct {
		mode, cause string
		code        int
	}{
		{"compile-enospc", "no space left on device", exitEnvironment},
		{"atomicwrite", "no space left on device", exitEnvironment},
		{"apply-fail", "input/output error", exitEnvironment},
	} {
		t.Run(tt.mode, func(t *testing.T) {
			cfg, count := fixture(t, tt.mode)
			code, out, errb := sweepAll(t, cfg)
			if code != tt.code {
				t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, tt.code, out, errb)
			}
			if n := calls(t, count); n != 1 {
				t.Errorf("mutate ran %d times: the sweep went on after the machine failed", n)
			}
			if strings.Contains(out, string(noBuild)) {
				t.Errorf("the failure was reported as %q:\n%s", noBuild, out)
			}
			if !strings.Contains(errb, "STOPPED") || !strings.Contains(errb, tt.cause) {
				t.Errorf("stderr does not say it stopped and why:\n%s", errb)
			}
			if !strings.Contains(errb, "no record was filed") {
				t.Errorf("stderr does not say nothing was filed:\n%s", errb)
			}
		})
	}
}

// The same words in a test's own output are a verdict about the code, not the disk.
func TestAMutantThatIsCaughtIsStillCaught(t *testing.T) {
	cfg, _ := fixture(t, "")
	gs, _, err := collect(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r := one(cfg, gs[0], []string{"go", "version"})
	if r.verdict != caught {
		t.Fatalf("verdict %q (%s), want caught", r.verdict, r.note)
	}
}

// 2: a restore that failed ends the sweep at once and names the files left mutated.
func TestAFailedRestoreAbortsAndNamesTheFilesLeftMutated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a read-only directory does not stop a file being created on Windows")
	}
	cfg, count := fixture(t, "restore-fail")
	t.Cleanup(func() { _ = os.Chmod(cfg.dir, 0o700) })
	code, out, errb := sweepAll(t, cfg)
	if code != exitRestore {
		t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, exitRestore, out, errb)
	}
	if n := calls(t, count); n != 1 {
		t.Errorf("mutate ran %d times: the next mutant was applied on top of a mutated file", n)
	}
	x := filepath.Join(cfg.dir, "x.go")
	if !strings.Contains(errb, "LEFT MUTATED: "+x) {
		t.Errorf("stderr does not list the file left mutated (%s):\n%s", x, errb)
	}
}

// mutate says it could not restore, but the sweep's own restore of last resort
// could: still a stop (the next mutant must not run on a file in doubt), and the
// report says nothing is left mutated rather than inventing a file.
func TestARestoreMutateCouldNotDoButTheSweepDidStillStops(t *testing.T) {
	cfg, count := fixture(t, "restore-fail-repairable")
	code, out, errb := sweepAll(t, cfg)
	if code != exitRestore || calls(t, count) != 1 {
		t.Fatalf("exit %d after %d runs, want %d after one\nstdout:\n%s\nstderr:\n%s", code, calls(t, count), exitRestore, out, errb)
	}
	if strings.Contains(errb, "LEFT MUTATED") || !strings.Contains(errb, "holds its original bytes") {
		t.Errorf("the report names a file that was put back, or says nothing:\n%s", errb)
	}
	if b, _ := os.ReadFile(filepath.Join(cfg.dir, "x.go")); string(b) != twoGuards {
		t.Errorf("the file was not put back:\n%s", b)
	}
}

// A mutate that exits happily and leaves the bytes changed in a writable directory:
// put back by the sweep, said in the note, not an abort.
func TestAMutationThatOutlivedMutateIsPutBackAndSaid(t *testing.T) {
	cfg, _ := fixture(t, "leaves-mutated")
	gs, _, err := collect(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r := one(cfg, gs[0], []string{"go", "version"})
	if r.verdict != caught || !strings.Contains(r.note, "put back here") {
		t.Fatalf("verdict %q, note %q", r.verdict, r.note)
	}
}

// A file the sweep cannot read is the machine's failure, and nothing is mutated.
func TestAGuardInAFileThatCannotBeReadIsAnEnvironmentFailure(t *testing.T) {
	cfg, count := fixture(t, "")
	gs, _, err := collect(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(cfg.dir, "x.go")); err != nil {
		t.Fatal(err)
	}
	r := one(cfg, gs[0], []string{"go", "version"})
	if r.verdict != envFail || calls(t, count) != 0 {
		t.Fatalf("verdict %q (%s) after %d runs, want %q after none", r.verdict, r.note, calls(t, count), envFail)
	}
}

func TestAvailableBytesAnswersOrFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("free space is not measured on this platform")
	}
	if n, err := availableBytes(t.TempDir()); err != nil || n == 0 {
		t.Errorf("availableBytes = %d, %v", n, err)
	}
	if _, err := availableBytes(filepath.Join(t.TempDir(), "no", "such")); err == nil {
		t.Error("a directory that is not there reported its free space")
	}
}

// A mutate that exits happily but leaves the bytes changed, in a directory nothing
// can be written to: the sweep's own restore of last resort fails.
func TestARestoreOfLastResortThatFailsAbortsTheSweep(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a read-only directory does not stop a file being created on Windows")
	}
	cfg, count := fixture(t, "walks-away")
	t.Cleanup(func() { _ = os.Chmod(cfg.dir, 0o700) })
	code, out, errb := sweepAll(t, cfg)
	if code != exitRestore {
		t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, exitRestore, out, errb)
	}
	if n := calls(t, count); n != 1 {
		t.Errorf("mutate ran %d times", n)
	}
	if !strings.Contains(errb, "LEFT MUTATED: "+filepath.Join(cfg.dir, "x.go")) {
		t.Errorf("the file left mutated is not named:\n%s", errb)
	}
}

// 3: the free-space precheck, with a stated threshold, injected through freeBytes.
func TestAFullDiskIsRefusedBeforeAndBetweenMutants(t *testing.T) {
	old := freeBytes
	t.Cleanup(func() { freeBytes = old })

	t.Run("before the first", func(t *testing.T) {
		cfg, count := fixture(t, "")
		cfg.minFreeMB = 1024
		freeBytes = func(string) (uint64, error) { return 100 << 20, nil }
		code, _, errb := sweepAll(t, cfg)
		if code != exitEnvironment || calls(t, count) != 0 {
			t.Fatalf("exit %d after %d runs, want %d after none\n%s", code, calls(t, count), exitEnvironment, errb)
		}
		if !strings.Contains(errb, "100 MiB free") || !strings.Contains(errb, "1024 MiB") {
			t.Errorf("the refusal does not state the measure and the threshold:\n%s", errb)
		}
	})

	t.Run("in the middle", func(t *testing.T) {
		cfg, count := fixture(t, "")
		cfg.minFreeMB = 1024
		// Plenty until one mutant has run, then the disk fills.
		freeBytes = func(string) (uint64, error) {
			if calls(t, count) >= 1 {
				return 0, nil
			}
			return 1 << 40, nil
		}
		code, out, errb := sweepAll(t, cfg)
		if code != exitEnvironment || calls(t, count) != 1 {
			t.Fatalf("exit %d after %d runs, want %d after one\nstdout:\n%s\nstderr:\n%s",
				code, calls(t, count), exitEnvironment, out, errb)
		}
	})

	t.Run("off, and unknown is not full", func(t *testing.T) {
		cfg, _ := fixture(t, "")
		cfg.minFreeMB = 0
		freeBytes = func(string) (uint64, error) { return 0, nil }
		if err := checkFreeSpace(cfg); err != nil {
			t.Errorf("0 must turn the check off: %v", err)
		}
		cfg.minFreeMB = 1024
		freeBytes = func(string) (uint64, error) { return 0, os.ErrInvalid }
		if err := checkFreeSpace(cfg); err != nil {
			t.Errorf("a file system that cannot be asked is not a full one: %v", err)
		}
	})
}

func TestMinFreeFlagDefault(t *testing.T) {
	cfg, _, code := parse([]string{"--", "true"}, &strings.Builder{})
	if code != 0 || cfg.minFreeMB != 1024 {
		t.Fatalf("code %d, min-free-mb %d, want 0 and 1024", code, cfg.minFreeMB)
	}
	cfg, _, _ = parse([]string{"-min-free-mb", strconv.Itoa(5), "--", "true"}, &strings.Builder{})
	if cfg.minFreeMB != 5 {
		t.Errorf("min-free-mb %d, want 5", cfg.minFreeMB)
	}
}
