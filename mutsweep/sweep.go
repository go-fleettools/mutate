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
)

// verdict is what one deleted guard did to the suite.
type verdict string

const (
	caught   verdict = "caught"   // the suite went red: the guard is held
	survived verdict = "SURVIVED" // the suite stayed green: nothing holds it
	noBuild  verdict = "not a mutant"
	unrun    verdict = "NOT RUN" // mutate itself could not be started
	hung     verdict = "HUNG"    // no answer in time: held, but only by a timeout
)

type result struct {
	guard
	verdict verdict
	took    time.Duration
	note    string
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
	base, code := baseline(cfg, cmd, stderr)
	if code != 0 {
		return code
	}
	fmt.Fprintf(stdout, "mutsweep: %d refusals, %v each at most, through %q (the suite itself takes %v)\n",
		len(guards), cfg.timeout, strings.Join(cmd, " "), base.Round(time.Millisecond))

	results := make([]result, 0, len(guards))
	for i, g := range guards {
		r := one(cfg, g, cmd)
		results = append(results, r)
		fmt.Fprintf(stdout, "[%d/%d] %-12s %s:%d  %s  %s\n",
			i+1, len(guards), r.verdict, g.file, g.line, trim(g.cond, 60), r.note)
	}
	return report(results, cfg.timeout, stdout, stderr)
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
func one(cfg config, g guard, cmd []string) result {
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
	switch {
	case ctx.Err() != nil:
		// Not survived: the guard is load-bearing, since the suite cannot
		// finish without it. Not caught either, because nothing SAYS so -- a
		// timeout names no cause and costs a runner its whole budget.
		r.verdict, r.note = hung, fmt.Sprintf("no answer in %v", cfg.timeout)
		// And the file must be back. mutate restores it when asked to stop, and
		// depending on another process having managed that is a hope: the
		// window where a source file holds a deliberate defect must not outlive
		// the sweep.
		if err := ensureRestored(path, before, readErr); err != nil {
			r.note += "; " + err.Error()
		}
	case err == nil:
		r.verdict, r.note = caught, took.Round(time.Second).String()
	case strings.Contains(string(out), "DID NOT COMPILE"):
		r.verdict, r.note = noBuild, firstBuildError(string(out))
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
	return r
}

func report(rs []result, timeout time.Duration, stdout, stderr io.Writer) int {
	counts := map[verdict]int{}
	for _, r := range rs {
		counts[r.verdict]++
	}
	fmt.Fprintf(stdout, "\n%d refusals: %d caught, %d not mutants, %d survived, %d hung\n",
		len(rs), counts[caught], counts[noBuild], counts[survived], counts[hung])
	if n := counts[unrun]; n > 0 {
		fmt.Fprintf(stderr, "::error::%d of them never ran: the mutate command could not be started\n", n)
	}

	// The two that need reading are listed again, because a line in the middle
	// of several hundred is a line nobody sees.
	var needed []result
	for _, r := range rs {
		if r.verdict == survived || r.verdict == hung {
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
		fmt.Fprintf(stderr, "  %-10s %s:%d  %s\n", r.verdict, r.file, r.line, trim(r.cond, 70))
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
	var count int
	for i := range s {
		if count == n-1 {
			return s[:i] + "…"
		}
		count++
	}
	return s
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
		return fmt.Errorf("RESTORE IMPOSSIBLE: %s was never read: %v", path, readErr)
	}
	now, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("RESTORE IMPOSSIBLE: cannot read %s: %v", path, err)
	}
	if bytes.Equal(now, before) {
		return nil
	}
	if err := os.WriteFile(path, before, 0o644); err != nil {
		return fmt.Errorf("RESTORE FAILED for %s: %w", path, err)
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
