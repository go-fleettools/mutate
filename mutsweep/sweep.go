package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-fleettools/mutate/internal/atomicwrite"
)

// verdict is what one deleted guard did to the suite.
type verdict string

const (
	caught   verdict = "caught"   // the suite went red: the guard is held
	survived verdict = "SURVIVED" // the suite stayed green: nothing holds it
	noBuild  verdict = "not a mutant"
	unrun    verdict = "NOT RUN" // mutate itself could not be started
	hung     verdict = "HUNG"    // no answer in time: held, but only by a timeout
	// caughtByPanic is red, but only through a runtime panic: no test failed by
	// an assertion. It is counted apart from caught and read like SURVIVED.
	caughtByPanic verdict = "caught by PANIC"
)

type result struct {
	guard
	verdict verdict
	took    time.Duration
	note    string
	// compileFail: the mutant did not compile (as opposed to any other reason for
	// noBuild), the one case worth retrying with the other mutation.
	compileFail bool
}

// marginOverBaseline is how much longer than the unmutated suite the timeout has
// to be before a HUNG verdict means anything.
//
// Three, measured against the two campaigns behind the numbers in the README. On
// go-crdt/crdt the suite took 14s under a 4m timeout -- seventeen times over --
// and its one HUNG was real: the guard bounded a walk over 2^62. On
// go-crdt/collab the suite took 60s under a 5m timeout, five times over, and SIX
// mutants came back HUNG. Two of those were then CAUGHT in seconds by their own
// targeted test, so nothing was hanging: the suite was finishing late, and a
// legitimate survivor in the same run took 4m22s of the 5m allowed. Three is
// what the thin case needed and what the roomy case had many times over.
const marginOverBaseline = 3

// sweep deletes each guard in turn and reports what the suite did about it.
func sweep(cfg config, guards []guard, cmd []string, stdout, stderr io.Writer) int {
	if err := refuseStale(cfg); err != nil {
		fmt.Fprintln(stderr, "mutsweep:", err)
		return 2
	}
	if err := supportsEcho(cfg.mutate); err != nil {
		fmt.Fprintln(stderr, "mutsweep:", err)
		return 2
	}
	base, code := baseline(cfg, cmd, stderr)
	if code != 0 {
		return code
	}
	fmt.Fprintf(stdout, "mutsweep: %d %s, %v each at most, through %q (the suite itself takes %v)\n",
		len(guards), cfg.noun(), cfg.timeout, strings.Join(cmd, " "), base.Round(time.Millisecond))
	method := byText
	if asksForJSON(cmd) {
		method = byJSON
	}
	fmt.Fprintf(stdout, "mutsweep: a red suite is read from %s, to tell an assertion from a panic\n", method)

	if err := checkFreeSpace(cfg); err != nil {
		fmt.Fprintln(stderr, "mutsweep:", err)
		return exitEnvironment
	}
	snap := takeSnapshot(cfg, guards)

	results := make([]result, 0, len(guards))
	for i, g := range guards {
		var r result
		// Before each mutant: the disk filled in the MIDDLE of a sweep, at #72 of 493.
		if err := checkFreeSpace(cfg); err != nil {
			r = result{guard: g, verdict: envFail, note: err.Error()}
		} else {
			r = one(cfg, g, cmd)
		}
		fmt.Fprintf(stdout, "[%d/%d] %-12s %s:%d  %s  %s\n",
			i+1, len(guards), r.verdict, g.file, g.line, trim(g.cond, 60), r.note)
		if r.verdict == envFail || r.verdict == restoreFail {
			// Never "not a mutant", never on to the next one, never a record.
			return abort(r, i+1, len(guards), snap, stderr)
		}
		results = append(results, r)
	}
	code = report(results, cfg.timeout, stdout, stderr)
	// Only a DELETE-mode sweep is filed: the push gate asks whether each refusal
	// was deleted with the suite watching, and a boundary sweep answers a
	// different question about the same lines.
	if !cfg.boundary() {
		if err := writeRecords(cfg, results, stderr); err != nil {
			// Said, and made to fail: a sweep whose record is missing is one
			// the gate will refuse, and a clean exit here would send the reader
			// looking for the fault in the gate.
			fmt.Fprintln(stderr, "mutsweep: the sweep ran but its record was NOT filed:", err)
			if code == 0 {
				code = 1
			}
		}
	}
	return code
}

// supportsEcho asks the mutate command whether it understands -echo, which
// mutsweep passes on every mutant.
//
// ⛔ An older mutate rejects the flag with exit status 2, which is also how it says
// "the mutant did not compile": every mutant of a sweep read as "not a mutant",
// which does not fail the gate, so a sweep that RAN NOTHING exited 0. Asked first,
// so that is refused before any time is spent.
func supportsEcho(bin string) error {
	out, err := exec.Command(bin, "-h").CombinedOutput()
	if err != nil && exitCode(err) < 0 {
		return fmt.Errorf("could not run %q: %v", bin, err)
	}
	if !strings.Contains(string(out), "-echo") {
		return fmt.Errorf("%q does not understand -echo, which mutsweep needs to tell a panic from an assertion "+
			"— every mutant would be rejected and nothing would run; install the mutate that comes with this mutsweep "+
			"(go install github.com/go-fleettools/mutate@latest)", bin)
	}
	return nil
}

// baseline runs the command once with nothing mutated, which is the measurement
// every verdict is relative to and which this tool did without until
// 2026-10-11.
//
// Two wrong answers come from not having it, and the first is the worse:
//
//   - ⛔ A SWEEP OVER AN ALREADY-RED SUITE REPORTS EVERY GUARD AS CAUGHT. The
//     command fails whatever is deleted, so every mutant looks held and the
//     summary is the strongest false reassurance there is: "0 survived".
//
//   - ⛔ A HUNG VERDICT IS A STATEMENT ABOUT THE TIME ALLOWED. On go-crdt/collab
//     a 60-second suite under a 5-minute timeout produced six HUNG, of which at
//     least two were simply slower -- caught in seconds by their own targeted
//     test. A legitimate survivor in the same run took 4m22s.
//
// It costs one run of the command, against one per guard.
func baseline(cfg config, cmd []string, stderr io.Writer) (time.Duration, int) {
	runIn := cfg.runIn
	if runIn == "" {
		runIn = cfg.dir
	}
	if runIn == "" {
		fmt.Fprintln(stderr, "mutsweep: no directory to run the command in")
		return 0, 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()
	c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	c.Dir = runIn
	// The same marker every mutant's command carries: a baseline that reached
	// this tool again would start a sweep of its own.
	c.Env = append(os.Environ(), active+"=1")
	ownGroup(c)

	start := time.Now()
	out, err := runBounded(ctx, c)
	took := time.Since(start)

	switch {
	case errors.Is(err, context.DeadlineExceeded):
		fmt.Fprintf(stderr, "mutsweep: the command did not finish in %v with NOTHING mutated, so no verdict "+
			"below it could mean anything — raise -timeout past what the suite itself takes\n%s\n",
			cfg.timeout, lastOf(out))
		return 0, 2
	case err != nil:
		fmt.Fprintf(stderr, "mutsweep: the command FAILS with nothing mutated, so every guard would be reported "+
			"as caught and the sweep would read as a clean one — fix the suite first\n%v\n%s\n",
			err, lastOf(out))
		return 0, 2
	}

	if least := marginOverBaseline * took; cfg.timeout < least {
		fmt.Fprintf(stderr, "mutsweep: the suite takes %v and -timeout is %v, under %d times it: a mutant that "+
			"merely makes the suite SLOWER would be reported HUNG, as six were on go-crdt/collab. "+
			"Use -timeout %v or more\n",
			took.Round(time.Millisecond), cfg.timeout, marginOverBaseline, least.Round(time.Second))
		return 0, 2
	}
	return took, 0
}

// lastOf is the end of a command's output, which is where a Go test suite says
// what failed.
func lastOf(b []byte) string {
	const keep = 600
	if len(b) <= keep {
		return strings.TrimSpace(string(b))
	}
	b = b[len(b)-keep:]
	// ⛔ A slice taken from the END can begin in the middle of a rune, and that
	// half rune makes the whole report invalid UTF-8. A log nobody can grep is
	// not a report: `grep SURVIVED chunk2.log` printed NOTHING over a sweep
	// holding three survivors, because grep had decided the file was binary --
	// and it does not say so on stdout, so the silence reads as "no survivors",
	// which is the strongest false reassurance a mutation sweep can give.
	for len(b) > 0 && !utf8.RuneStart(b[0]) {
		b = b[1:]
	}
	return "…" + strings.TrimSpace(string(b))
}

// one runs a single deletion through mutate, which owns applying it and putting
// the file back -- including when this process is interrupted while it waits.
//
// Deleting an `if` can leave a variable used only in its condition unused, and
// the mutant then fails to compile: 8 of 11 refusals in one file read "not a
// mutant" that way, which is a guard nothing could test. So a deletion that does
// not compile is retried as the condition replaced by `false && (C)`, which keeps
// every use and makes the body unreachable.
func one(cfg config, g guard, cmd []string) result {
	r := oneMutation(cfg, g, cmd)
	if r.verdict != noBuild || !r.compileFail || g.altFrom == "" {
		return r
	}
	alt := g
	alt.from, alt.to, alt.altFrom, alt.altTo = g.altFrom, g.altTo, "", ""
	r2 := oneMutation(cfg, alt, cmd)
	r2.guard = g
	if r2.verdict == noBuild {
		r2.note = "neither the deletion nor false && (C) compiles: " + r2.note
	} else {
		r2.note = "by false && (C), deleting it does not compile; " + r2.note
	}
	return r2
}

func oneMutation(cfg config, g guard, cmd []string) result {
	// The zero value must be safe: a config built by hand with no run-in once
	// ran the command in THIS tool's directory, where `go test ./...` is this
	// tool's own suite, and the mutant was reported HUNG after two minutes of
	// recursion.
	runIn := cfg.runIn
	if runIn == "" {
		runIn = cfg.dir
	}
	if runIn == "" {
		// Never the current directory by accident: that is how the command
		// came to run where this tool's own tests live.
		return result{guard: g, verdict: noBuild, note: "no directory to run in"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()

	argv := append([]string{
		// -echo: a caught mutant's output is what says whether it went red by an
		// assertion or by a panic, and mutate prints it only when asked.
		"-echo",
		"-file", g.pathFrom(runIn),
		"-from", g.from,
		"-to", g.to,
		"-name", fmt.Sprintf("%s:%d %s", g.file, g.line, trim(g.cond, 40)),
		"--",
	}, cmd...)

	// The bytes as they are now, so a restore cannot depend on finding text.
	// It must not: when a guard's text is unique the replacement is EMPTY, and
	// putting it back by matching that empty string inserts the guard at the
	// start of the file. Windows showed it, because there mutate was not asked
	// politely enough to restore the file itself and this ran for real.
	path := filepath.Join(runIn, g.pathFrom(runIn))
	before, readErr := os.ReadFile(path)
	if readErr != nil {
		// Nothing to put back would be nothing to compare with: do not mutate a
		// file this tool cannot read. Not "not a mutant" -- a statement about the machine.
		return result{guard: g, verdict: envFail, note: fmt.Sprintf("cannot read %s: %v", path, readErr)}
	}

	start := time.Now()
	c := exec.Command(cfg.mutate, argv...)
	c.Env = append(os.Environ(), active+"=1")
	// The package under test is where the command has to run: `go test ./...`
	// means nothing from the sweeping tool's own directory, and the first
	// version of this reported every mutant "caught" in zero seconds because
	// the command failed before it ever reached the package.
	c.Dir = runIn
	ownGroup(c)
	out, err := runBounded(ctx, c)
	took := time.Since(start)

	r := result{guard: g, took: took}

	// The file must be back, whatever happened. mutate restores it, and depending on
	// another process having managed that is a hope: the window where a source file
	// holds a deliberate defect must not outlive the sweep -- and a restore that
	// failed stops the sweep (restoreFail), it is never one note among others.
	restoreErr := ensureRestored(path, before, readErr)
	var rf *restoreFailure
	if errors.As(restoreErr, &rf) {
		return result{guard: g, took: took, verdict: restoreFail, note: rf.Error()}
	}
	if exitCode(err) == mutateRestore {
		return result{guard: g, took: took, verdict: restoreFail,
			note: "mutate could not put " + path + " back: " + trim(strings.TrimSpace(lastLine(string(out))), 100)}
	}
	// What the machine says, before what the command says: an ENOSPC in a build is
	// no statement about the guard.
	if ctx.Err() == nil {
		if exitCode(err) == mutateEnvironment {
			return result{guard: g, took: took, verdict: envFail,
				note: "mutate could not apply the mutation: " + trim(strings.TrimSpace(lastLine(string(out))), 100)}
		}
		if cause := environmentCause(string(out)); cause != "" {
			return result{guard: g, took: took, verdict: envFail, note: cause}
		}
	}

	switch {
	case ctx.Err() != nil:
		// Not survived: the guard is load-bearing, since the suite cannot
		// finish without it. Not caught either, because nothing SAYS so -- a
		// timeout names no cause and costs a runner its whole budget.
		r.verdict, r.note = hung, fmt.Sprintf("no answer in %v", cfg.timeout)
		if restoreErr != nil {
			r.note += "; " + restoreErr.Error()
		}
	case err == nil:
		var by how
		r.verdict, by = classify(out, asksForJSON(cmd))
		r.note = took.Round(time.Second).String()
		if r.verdict == caughtByPanic {
			r.note += "  no test failed by an assertion (read from " + string(by) + ")"
			if by == byText {
				// Plain output without -v prints a test's lines only when it
				// finishes, so a parallel test's assertion can be lost to the
				// panic: this PANIC may be a false alarm, never the reverse.
				r.note += "; plain output can lose an assertion, re-run with go test -json to confirm"
			}
		}
	case exitCode(err) == 2 && strings.HasPrefix(string(out), "flag provided but not defined"):
		// mutate's OWN refusal, which is the first thing it prints and exits 2.
		// Never the command's output: a CLI's flag tests print this phrase too,
		// and a survivor must not turn into NOT RUN because of them.
		// A mutate that does not know a flag mutsweep passes: nothing ran, and
		// that is a statement about this machine, never about the code.
		r.verdict, r.note = unrun, "mutate rejected a flag: "+trim(strings.TrimSpace(lastLine(string(out))), 70)
	case strings.Contains(string(out), "DID NOT COMPILE"):
		r.verdict, r.note, r.compileFail = noBuild, firstBuildError(string(out)), true
	case exitCode(err) == 1:
		r.verdict, r.note = survived, took.Round(time.Second).String()
	case exitCode(err) < 0:
		// The command never started: a missing binary, the wrong name on a
		// platform that wants an extension. Calling that "not a mutant" would
		// read as a statement about the code, and it is a statement about this
		// machine -- Windows CI said it first, with an empty note.
		r.verdict, r.note = unrun, fmt.Sprintf("could not run %q: %v", cfg.mutate, err)
	default:
		r.verdict, r.note = noBuild, trim(strings.TrimSpace(lastLine(string(out))), 70)
	}
	if restoreErr != nil && r.verdict != hung {
		r.note += "; " + restoreErr.Error()
	}
	return r
}

func report(rs []result, timeout time.Duration, stdout, stderr io.Writer) int {
	counts := map[verdict]int{}
	for _, r := range rs {
		counts[r.verdict]++
	}
	fmt.Fprintf(stdout, "\n%d mutations: %d caught, %d caught by PANIC, %d not mutants, %d survived, %d hung\n",
		len(rs), counts[caught], counts[caughtByPanic], counts[noBuild], counts[survived], counts[hung])
	if n := counts[unrun]; n > 0 {
		fmt.Fprintf(stderr, "::error::%d of them never ran: the mutate command could not be started\n", n)
	}

	// The two that need reading are listed again, because a line in the middle
	// of several hundred is a line nobody sees.
	var needed []result
	for _, r := range rs {
		if r.verdict == survived || r.verdict == hung || r.verdict == caughtByPanic {
			needed = append(needed, r)
		}
	}
	if counts[unrun] > 0 {
		return 1
	}
	if len(needed) == 0 {
		return 0
	}
	sort.Slice(needed, func(i, j int) bool { return needed[i].verdict < needed[j].verdict })
	fmt.Fprintln(stderr, "\nwhat nothing holds:")
	for _, r := range needed {
		fmt.Fprintf(stderr, "  %-16s %s:%d  %s\n", r.verdict, r.file, r.line, trim(r.cond, 70))
	}
	if counts[caughtByPanic] > 0 {
		fmt.Fprintln(stderr, "\nA PANIC red is luck, not a test: the suite went red because the code after the")
		fmt.Fprintln(stderr, "mutation crashed, and no assertion says what the guard is for. The next change")
		fmt.Fprintln(stderr, "that enlarges the table or adds a check turns it into a survivor, silently.")
		fmt.Fprintln(stderr, "Write the test that asserts the value on both sides of the guard.")
	}
	if counts[hung] > 0 {
		if slowest, close := nearTheLimit(rs, timeout); close {
			fmt.Fprintf(stderr, "\n⚠ A mutant that ANSWERED took %v of the %v allowed, so the %d HUNG above may be\n"+
				"slower rather than stuck. Re-run them alone against a test that names the guard:\n"+
				"  mutsweep -only %s -timeout %v -- <a command that exercises it>\n",
				slowest.Round(time.Second), timeout, counts[hung], onlyList(needed), 4*timeout)
		}
	}
	fmt.Fprintln(stderr, "\nA survivor is not yet a defect: it may be a bound doubled one layer down,")
	fmt.Fprintln(stderr, "a fast path, or the same value by another line. Read each one before")
	fmt.Fprintln(stderr, "writing a test, and read the END of the function -- most of them are held")
	fmt.Fprintln(stderr, "by something further in.")
	return 1
}

func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func firstBuildError(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, ".go:") && strings.Contains(line, ":") {
			return trim(strings.TrimSpace(line), 70)
		}
	}
	return "did not compile"
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

// trim bounds a field to n characters -- RUNES, not bytes.
//
// ⛔ Cutting at a byte offset splits a multi-byte character in half, and this
// tool's own output is full of them: the ⛔ and … it prints, and the Go source
// it quotes back. One half rune makes the report invalid UTF-8, and grep then
// treats the whole file as binary and matches in silence. See lastOf.
func trim(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	// Past the first n-1 runes. A byte that is not valid UTF-8 counts as one
	// rune of width 1, which is what ranging over the string does too.
	cut := 0
	for range n - 1 {
		_, width := utf8.DecodeRuneInString(s[cut:])
		cut += width
	}
	return s[:cut] + "…"
}

// runBounded runs the command and, if the context ends first, kills what it
// started rather than only the command itself.
//
// exec.CommandContext would kill the direct child alone, leaving the `go test`
// that mutate spawned to hold its temporary directory and outlive the sweep.
func runBounded(ctx context.Context, c *exec.Cmd) ([]byte, error) {
	var buf bytes.Buffer
	c.Stdout, c.Stderr = &buf, &buf
	if err := c.Start(); err != nil {
		return buf.Bytes(), err
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		return buf.Bytes(), err
	case <-ctx.Done():
		killTree(c)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			hardKill(c)
			<-done
		}
		return buf.Bytes(), ctx.Err()
	}
}

// ensureRestored writes the file back as it was, if it is not already.
//
// By BYTES, never by matching text: the replacement for a uniquely spelled
// guard is the empty string, and restoring by matching that inserts the guard
// wherever the empty string is first "found", which is offset zero.
func ensureRestored(path string, before []byte, readErr error) error {
	if readErr != nil {
		return &restoreFailure{path, fmt.Sprintf("RESTORE IMPOSSIBLE: %s was never read: %v", path, readErr)}
	}
	now, err := os.ReadFile(path)
	if err != nil {
		return &restoreFailure{path, fmt.Sprintf("RESTORE IMPOSSIBLE: cannot read %s: %v", path, err)}
	}
	if bytes.Equal(now, before) {
		return nil
	}
	// ⛔ ATOMICALLY. This is the restore of last resort — it runs when mutate
	// did not restore the file itself, which is exactly the case where
	// something killed mutate. Writing it back with a truncating write gives
	// the kill a second window to land in, on the very path that is already
	// known to be at risk.
	// The perm is a fallback for a file that is not there, which this path has
	// just read: atomicwrite keeps the mode the file has.
	if err := atomicwrite.Write(path, before, 0o644); err != nil {
		return &restoreFailure{path, fmt.Sprintf("RESTORE FAILED for %s: %v", path, err)}
	}
	return fmt.Errorf("the mutation outlived mutate and was put back here")
}

// nearTheLimit reports the longest run that actually ANSWERED, and whether it
// came close enough to the timeout to make a HUNG verdict in the same sweep
// untrustworthy.
//
// This is the signal the margin check cannot give. On go-crdt/collab the suite
// took 60s under a 5m timeout -- five times over, which marginOverBaseline
// allows -- and six mutants still came back HUNG, while a legitimate SURVIVOR
// in the same run took 4m22s. The ratio that mattered was not timeout over
// baseline but timeout over the slowest mutant that finished.
func nearTheLimit(rs []result, timeout time.Duration) (time.Duration, bool) {
	var slowest time.Duration
	for _, r := range rs {
		if r.verdict != hung && r.took > slowest {
			slowest = r.took
		}
	}
	// Two thirds: at that point a mutant only half again slower than the
	// slowest one that answered would be called HUNG.
	return slowest, slowest*3 > timeout*2
}

// onlyList renders the -only argument that re-asks about the hung ones.
func onlyList(rs []result) string {
	var at []string
	for _, r := range rs {
		if r.verdict == hung {
			at = append(at, fmt.Sprintf("%s:%d", r.file, r.line))
		}
	}
	return strings.Join(at, ",")
}
