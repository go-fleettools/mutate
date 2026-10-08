package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// verdict is what one deleted guard did to the suite.
type verdict string

const (
	caught   verdict = "caught"   // the suite went red: the guard is held
	survived verdict = "SURVIVED" // the suite stayed green: nothing holds it
	noBuild  verdict = "not a mutant"
	hung     verdict = "HUNG" // no answer in time: held, but only by a timeout
)

type result struct {
	guard
	verdict verdict
	took    time.Duration
	note    string
}

// sweep deletes each guard in turn and reports what the suite did about it.
func sweep(cfg config, guards []guard, cmd []string, stdout, stderr io.Writer) int {
	fmt.Fprintf(stdout, "mutsweep: %d refusals, %v each at most, through %q\n",
		len(guards), cfg.timeout, strings.Join(cmd, " "))

	results := make([]result, 0, len(guards))
	for i, g := range guards {
		r := one(cfg, g, cmd)
		results = append(results, r)
		fmt.Fprintf(stdout, "[%d/%d] %-12s %s:%d  %s  %s\n",
			i+1, len(guards), r.verdict, g.file, g.line, trim(g.cond, 60), r.note)
	}
	return report(results, stdout, stderr)
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

	start := time.Now()
	c := exec.CommandContext(ctx, cfg.mutate, argv...)
	c.Env = append(os.Environ(), active+"=1")
	// The package under test is where the command has to run: `go test ./...`
	// means nothing from the sweeping tool's own directory, and the first
	// version of this reported every mutant "caught" in zero seconds because
	// the command failed before it ever reached the package.
	c.Dir = runIn
	out, err := c.CombinedOutput()
	took := time.Since(start)

	r := result{guard: g, took: took}
	switch {
	case ctx.Err() != nil:
		// Not survived: the guard is load-bearing, since the suite cannot
		// finish without it. Not caught either, because nothing SAYS so -- a
		// timeout names no cause and costs a runner its whole budget.
		r.verdict, r.note = hung, fmt.Sprintf("no answer in %v", cfg.timeout)
	case err == nil:
		r.verdict, r.note = caught, took.Round(time.Second).String()
	case strings.Contains(string(out), "DID NOT COMPILE"):
		r.verdict, r.note = noBuild, firstBuildError(string(out))
	case exitCode(err) == 1:
		r.verdict, r.note = survived, took.Round(time.Second).String()
	default:
		r.verdict, r.note = noBuild, trim(strings.TrimSpace(lastLine(string(out))), 70)
	}
	return r
}

func report(rs []result, stdout, stderr io.Writer) int {
	counts := map[verdict]int{}
	for _, r := range rs {
		counts[r.verdict]++
	}
	fmt.Fprintf(stdout, "\n%d refusals: %d caught, %d not mutants, %d survived, %d hung\n",
		len(rs), counts[caught], counts[noBuild], counts[survived], counts[hung])

	// The two that need reading are listed again, because a line in the middle
	// of several hundred is a line nobody sees.
	var needed []result
	for _, r := range rs {
		if r.verdict == survived || r.verdict == hung {
			needed = append(needed, r)
		}
	}
	if len(needed) == 0 {
		return 0
	}
	sort.Slice(needed, func(i, j int) bool { return needed[i].verdict < needed[j].verdict })
	fmt.Fprintln(stderr, "\nwhat nothing holds:")
	for _, r := range needed {
		fmt.Fprintf(stderr, "  %-10s %s:%d  %s\n", r.verdict, r.file, r.line, trim(r.cond, 70))
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

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
