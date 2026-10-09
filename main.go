// Command mutate proves that a test can fail.
//
// # WHY THIS EXISTS
//
// A green test says nothing on its own. It says something once you have
// seen it go red for the reason it claims to guard — and the only way to
// see that is to break the code on purpose and watch. I had been doing
// that by hand, with a throwaway `perl -0pi -e` per mutation, roughly
// twenty-five times in one session: each one an ad-hoc regex, an ad-hoc
// restore, and no record afterwards of which mutations were actually
// tried.
//
// Three things went wrong in those twenty-five, and each is now impossible
// here rather than a matter of care:
//
//	the regex did not match      and the "mutation" ran against unchanged
//	                             code, so the test passed and I read that
//	                             as the test being weak
//	the mutant did not compile   which is not a failing test, it is no test
//	                             at all, and reads identically in a grep
//	                             for FAIL
//	the restore was forgotten    leaving a deliberate defect in a tree I
//	                             then committed from
//
// # WHAT IT DOES
//
//	mutate -file X.go -from "<exact text>" -to "<exact text>" -- go test -run Y ./...
//
// It replaces exact text (never a pattern: a regex that half-matches is
// the first failure above), runs the command, and restores the file
// whatever happens — including on SIGINT, because the window where a
// source file holds a deliberate defect must not outlive the process.
//
// It exits 0 only when the command FAILED, which is the point: a mutation
// the tests survive is a hole, and this says so in those words.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(argv []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mutate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "the source file to mutate")
	from := fs.String("from", "", "exact text to replace (not a pattern)")
	to := fs.String("to", "", "what to replace it with")
	name := fs.String("name", "", "what this mutation is, for the report")
	wantPass := fs.Bool("expect-pass", false,
		"invert: succeed when the command still PASSES. For a mutation that must NOT matter")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	cmd := fs.Args()
	if *file == "" || *from == "" || len(cmd) == 0 {
		fmt.Fprintln(stderr, "usage: mutate -file F -from TEXT -to TEXT [-name N] -- command...")
		return 2
	}

	m, err := newMutation(*file, *from, *to, *name)
	if err != nil {
		fmt.Fprintln(stderr, "mutate:", err)
		return 2
	}
	defer m.restore()
	// A deliberate defect in a source file must not outlive this process,
	// and ^C during a slow `go test` is exactly when it would.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() { <-sig; m.restore(); os.Exit(130) }()

	if err := m.apply(); err != nil {
		fmt.Fprintln(stderr, "mutate:", err)
		return 2
	}

	c := exec.Command(cmd[0], cmd[1:]...)
	out, runErr := c.CombinedOutput()
	m.restore()

	// A mutant that does not COMPILE is not a failing test, it is no test
	// at all — and in a grep for FAIL the two look the same. Told apart
	// here, because a compile error means the mutation was the wrong
	// shape and has to be rewritten, not that the suite is sound.
	if looksLikeBuildFailure(string(out)) {
		fmt.Fprintf(stderr, "mutate: %s DID NOT COMPILE — rewrite the mutation, this proves nothing\n", m.label())
		fmt.Fprint(stderr, indent(string(out)))
		return 2
	}

	passed := runErr == nil
	if passed == *wantPass {
		fmt.Fprintf(stdout, "mutate: %s — %s\n", m.label(), verdict(*wantPass))
		return 0
	}
	if *wantPass {
		fmt.Fprintf(stderr, "mutate: %s — the suite FAILED on a change that should not matter\n", m.label())
	} else {
		fmt.Fprintf(stderr, "mutate: %s — THE SUITE SURVIVED IT. Nothing covers this.\n", m.label())
	}
	fmt.Fprint(stderr, indent(string(out)))
	return 1
}

func verdict(wantPass bool) string {
	if wantPass {
		return "the suite still passes, as it should"
	}
	return "caught"
}

// mutation holds the original bytes, so a restore cannot depend on being
// able to reverse the edit.
type mutation struct {
	path, from, to, name string
	original             []byte
	applied              bool
}

func newMutation(path, from, to, name string) (*mutation, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	n := strings.Count(string(b), from)
	switch n {
	case 0:
		// THE FIRST FAILURE this tool exists to stop: a mutation that
		// matches nothing runs against unchanged code, the tests pass,
		// and the reading is "the test is weak" when the truth is "the
		// mutation never happened".
		return nil, fmt.Errorf("%s: the text to replace is not there — this mutation would be a no-op", path)
	case 1:
	default:
		// Replacing all of them is a different, larger mutation than the
		// one being described, and the report would name the small one.
		return nil, fmt.Errorf("%s: the text to replace appears %d times — make it unique", path, n)
	}
	return &mutation{path: path, from: from, to: to, name: name, original: b}, nil
}

func (m *mutation) label() string {
	if m.name != "" {
		return m.name
	}
	return strings.TrimSpace(firstLine(m.from))
}

func (m *mutation) apply() error {
	fi, err := os.Stat(m.path)
	if err != nil {
		return err
	}
	out := strings.Replace(string(m.original), m.from, m.to, 1)
	if err := os.WriteFile(m.path, []byte(out), fi.Mode().Perm()); err != nil {
		return err
	}
	m.applied = true
	return nil
}

func (m *mutation) restore() {
	if !m.applied {
		return
	}
	m.applied = false
	if err := os.WriteFile(m.path, m.original, 0o644); err != nil {
		// Said as loudly as possible: the tree now holds a deliberate
		// defect and the next commit would carry it.
		fmt.Fprintf(os.Stderr, "mutate: COULD NOT RESTORE %s: %v\n", m.path, err)
	}
}

// compilerSays are phrases the Go toolchain prints when a package will not
// build. Every one of them is also a phrase a TEST may print, which is the
// whole difficulty.
var compilerSays = []string{
	"build constraints exclude", "undefined:", "syntax error",
	"declared and not used", "cannot use", "not enough arguments",
	"too many arguments", "imported and not used",
}

// looksLikeBuildFailure distinguishes "the test failed" from "nothing ran".
//
// ⛔ It looks at WHERE the phrase appears, not merely whether it appears.
// Searching the whole output got this wrong in the direction that costs most:
// a go-odf test failed with
//
//	limits_test.go:85: it was refused by *xml.SyntaxError (XML syntax error …)
//
// and "syntax error" matched. A mutation the suite had CAUGHT — three tests
// failed on it — was reported as "DID NOT COMPILE … this proves nothing",
// which invites rewriting a mutation that was already working, or concluding a
// guard is untested when it is not.
//
// The rule is the shape of the output rather than its words. `go test` prints
// compiler errors UNINDENTED, under a "# package" header, before the summary;
// everything a test itself says is indented, or carried on a line beginning
// ---, ===, ok, PASS or FAIL. So a phrase only counts when it is on a line the
// COMPILER could have written.
//
// "[build failed]" stays decisive wherever it is: it is the test runner's own
// word for this, and it appears on a FAIL summary line.
func looksLikeBuildFailure(out string) bool {
	if strings.Contains(out, "[build failed]") {
		return true
	}
	for _, line := range strings.Split(out, "\n") {
		if !compilerCouldHaveWritten(line) {
			continue
		}
		for _, s := range compilerSays {
			if strings.Contains(line, s) {
				return true
			}
		}
	}
	return false
}

// compilerCouldHaveWritten says whether a line is the toolchain speaking
// rather than a test. Indented lines are a test's own output; the rest are the
// runner's summary lines, which are recognisable by how they begin.
func compilerCouldHaveWritten(line string) bool {
	if line == "" || line[0] == ' ' || line[0] == '\t' {
		return false
	}
	for _, p := range []string{"--- ", "=== ", "ok ", "ok\t", "PASS", "FAIL", "? ", "?\t"} {
		if strings.HasPrefix(line, p) {
			return false
		}
	}
	return true
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func indent(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		b.WriteString("    " + line + "\n")
	}
	return b.String()
}
