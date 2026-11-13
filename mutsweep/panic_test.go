package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTheMethodIsNamed(t *testing.T) {
	ev := `{"Action":"fail","Package":"x","Test":"TestA"}` + "\n"
	if _, m := classify([]byte(ev), true); m != byJSON {
		t.Errorf("events read as %q", m)
	}
	if _, m := classify([]byte("--- FAIL: TestA\n"), false); m != byText {
		t.Errorf("text read as %q", m)
	}
	if _, m := classify([]byte("--- FAIL: TestA\n"), true); m != byText {
		t.Errorf("a command that said -json but spoke text must be reported as read from text, got %q", m)
	}
}

func TestOnlyGoTestWithJSONIsReadAsEvents(t *testing.T) {
	for cmd, want := range map[string]bool{
		"go test -json ./...":             true,
		"go test -count 1 -json ./...":    true,
		"/usr/local/go/bin/go test -json": true,
		"go test -json=true ./...":        true,
		"go test ./...":                   false,
		"go vet -json ./...":              false,
		"make test -json":                 false,
		"go test -run X -- -json":         false,
		"go":                              false,
	} {
		if got := asksForJSON(strings.Fields(cmd)); got != want {
			t.Errorf("%q: asksForJSON = %v, want %v", cmd, got, want)
		}
	}
}

// A panic red needs reading and fails a CI gate, like a survivor does.
func TestAPanicRedFailsTheSweepAndIsListed(t *testing.T) {
	var out, errOut strings.Builder
	rs := []result{{verdict: caught}, {guard: guard{file: "kn.go", line: 66, cond: "x >= 512  [>= to >]"}, verdict: caughtByPanic}}
	if code := report(rs, time.Minute, &out, &errOut); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(out.String(), "1 caught, 1 caught by PANIC") {
		t.Errorf("the panic is not counted apart:\n%s", out.String())
	}
	if !strings.Contains(errOut.String(), "what nothing holds:") || !strings.Contains(errOut.String(), "caught by PANIC") ||
		!strings.Contains(errOut.String(), "kn.go:66") {
		t.Errorf("the panic is not listed under what nothing holds:\n%s", errOut.String())
	}
}

// ⛔ A mutate that does not know -echo rejects every mutant with exit status 2,
// the same status as "did not compile", and a gate that ran nothing exited 0.
func TestAnOldMutateIsRefusedBeforeAnythingRuns(t *testing.T) {
	dir := t.TempDir()
	old := buildOldMutate(t)
	write(t, dir, "go.mod", "module x\n\ngo 1.27.1\n")
	write(t, dir, "x.go", "package x\n\nfunc F(n int) int {\n\tif n < 0 {\n\t\treturn 0\n\t}\n\treturn n\n}\n")
	var out, errOut strings.Builder
	code := run([]string{"-dir", dir, "-mutate-bin", old, "--", "true"}, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit %d, want 2:\n%s%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "-echo") {
		t.Errorf("the refusal does not name the flag: %q", errOut.String())
	}
	if strings.Contains(out.String(), "refusals,") {
		t.Errorf("a sweep started: %q", out.String())
	}

	// And one that slips past the check is still a failing gate, never a pass.
	g := oneGuardOf(t, dir)
	got := one(config{dir: dir, timeout: time.Minute, mutate: old}, g[0], []string{"true"})
	if got.verdict != unrun {
		t.Fatalf("verdict %q (%s), want %q", got.verdict, got.note, unrun)
	}
	if code := report([]result{got}, time.Minute, &out, &errOut); code != 1 {
		t.Fatalf("report exit %d, want 1", code)
	}
}

// ⛔ "flag provided but not defined" is mutate's own refusal only when mutate
// says it. A CLI under test whose flag tests print the phrase must not turn a
// SURVIVOR into NOT RUN: the guard here is held by nothing, and the command's
// output (shown by go test -v, and echoed by mutate on a survivor) holds it.
func TestAFlagErrorInTheCommandsOutputIsNotMutatesRefusal(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module x\n\ngo 1.27.1\n")
	write(t, dir, "x.go", "package x\n\nfunc F(n int) int {\n\tif n < 0 {\n\t\treturn 0\n\t}\n\treturn n\n}\n")
	write(t, dir, "x_test.go", "package x\n\nimport \"testing\"\n\nfunc TestFlags(t *testing.T) {\n"+
		"\tt.Log(\"flag provided but not defined: -nope\")\n\tif F(1) != 1 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n")
	g := oneGuardOf(t, dir)
	for _, cmd := range [][]string{
		{"go", "test", "-count", "1", "-v", "./..."},
		{"go", "test", "-count", "1", "-v", "-json", "./..."},
	} {
		got := one(config{dir: dir, timeout: 2 * time.Minute, mutate: buildMutate(t)}, g[0], cmd)
		if got.verdict != survived {
			t.Fatalf("%v: verdict %q (%s), want %q", cmd, got.verdict, got.note, survived)
		}
	}
}

// buildOldMutate compiles a stand-in that behaves as a mutate from before -echo:
// the same flags but that one, and exit 2 on any other, as the flag package does.
func buildOldMutate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	const prog = `package main

import (
	"flag"
	"os"
)

func main() {
	fs := flag.NewFlagSet("mutate", flag.ContinueOnError)
	fs.String("file", "", "")
	fs.String("from", "", "")
	fs.String("to", "", "")
	fs.String("name", "", "")
	fs.Bool("expect-pass", false, "")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
}
`
	write(t, dir, "main.go", prog)
	write(t, dir, "go.mod", "module oldmutate\n\ngo 1.27.1\n")
	bin := filepath.Join(t.TempDir(), "oldmutate"+exeSuffix())
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the stand-in: %v\n%s", err, out)
	}
	return bin
}

// Four guards, four ways of being held, four verdicts -- through the real mutate
// and the real go tool, on a copy so the fixture is never the thing mutated.
func TestEachBoundaryGetsItsOwnVerdict(t *testing.T) {
	mutateBin := buildMutate(t)
	for _, cmd := range [][]string{
		{"go", "test", "-count", "1", "-json", "./..."},
		{"go", "test", "-count", "1", "./..."},
	} {
		t.Run(strings.Join(cmd[3:], " "), func(t *testing.T) {
			dir := t.TempDir()
			if err := os.CopyFS(dir, os.DirFS("testdata/edges")); err != nil {
				t.Fatal(err)
			}
			guards, _, err := collect(config{dir: dir, mode: modeBoundary})
			if err != nil {
				t.Fatal(err)
			}
			if len(guards) != 4 {
				t.Fatalf("found %d comparisons, want 4: %+v", len(guards), guards)
			}
			want := map[string]verdict{
				"n > 9":   caught,
				"x >= 4":  caughtByPanic,
				"n >= 1":  hung,
				"x > 100": survived,
			}
			for _, g := range guards {
				timeout := 2 * time.Minute
				if strings.HasPrefix(g.cond, "n >= 1") {
					timeout = 3 * time.Second
				}
				got := one(config{dir: dir, timeout: timeout, mutate: mutateBin, mode: modeBoundary}, g, cmd)
				key := strings.TrimSpace(g.cond[:strings.Index(g.cond, "  [")])
				if w, ok := want[key]; !ok || got.verdict != w {
					t.Errorf("%s: verdict %q (%s), want %q", g.cond, got.verdict, got.note, w)
				}
			}
		})
	}
}

func TestBoundaryMovesEveryComparisonAndNothingElse(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module x\n\ngo 1.27.1\n")
	write(t, dir, "x.go", `package x

func F(a, b, c int, s []int) int {
	if a < b && b >= c {
		return 1
	}
	if a == b || a != c {
		return 2
	}
	for i := 0; i <= len(s); i++ {
		_ = i
	}
	for range s {
	}
	for i := len(s); i > 0; i-- {
		_ = i
	}
	return 0
}
`)
	got, _, err := collect(config{dir: dir, mode: modeBoundary})
	if err != nil {
		t.Fatal(err)
	}
	var conds []string
	for _, g := range got {
		conds = append(conds, g.cond)
	}
	want := []string{
		"a < b  [< to <=]", "b >= c  [>= to >]",
		"i <= len(s)  [<= to <]", "i > 0  [> to >=]",
	}
	if strings.Join(conds, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q\nwant %q", conds, want)
	}
	// The default is still deletion: the two refusals, whatever their operators.
	del, _, err := collect(config{dir: dir})
	if err != nil || len(del) != 2 {
		t.Fatalf("deletion found %d refusals (%v), want 2", len(del), err)
	}
}

// Two identical guards are both addressable: each mutation is widened until it
// occurs once, so mutate does not refuse it as ambiguous.
func TestIdenticalComparisonsAreStillAddressable(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module x\n\ngo 1.27.1\n")
	write(t, dir, "x.go", `package x

func A(n int) int {
	if n < 3 {
		return 1
	}
	return 0
}

func B(n int) int {
	if n < 3 {
		return 1
	}
	return 0
}
`)
	got, _, err := collect(config{dir: dir, mode: modeBoundary})
	if err != nil || len(got) != 2 {
		t.Fatalf("found %d (%v), want 2", len(got), err)
	}
	src, _ := os.ReadFile(dir + "/x.go")
	for _, g := range got {
		if n := strings.Count(string(src), g.from); n != 1 {
			t.Errorf("line %d: from occurs %d times, want 1", g.line, n)
		}
		if g.to == g.from || !strings.HasSuffix(g.to, "<=") {
			t.Errorf("line %d: to %q does not move the edge of %q", g.line, g.to, g.from)
		}
	}
}

func TestOnlyNamesAComparisonByItsLine(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module x\n\ngo 1.27.1\n")
	write(t, dir, "x.go", `package x

func F(a, b, c int) int {
	if a < b && b >= c {
		return 1
	}
	return 0
}
`)
	got, _, err := collect(config{dir: dir, mode: modeBoundary, only: []target{{"x.go", 4}}})
	if err != nil || len(got) != 2 {
		t.Fatalf("a line holding two comparisons gave %d (%v), want both", len(got), err)
	}
}

func TestAMutateModeThatIsNeitherIsRefused(t *testing.T) {
	var errOut strings.Builder
	if _, _, code := parse([]string{"-mutate", "mutate", "--", "true"}, &errOut); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "-mutate-bin") {
		t.Errorf("the refusal does not say where the command went: %q", errOut.String())
	}
}
