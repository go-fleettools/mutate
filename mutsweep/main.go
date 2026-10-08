// Command mutsweep deletes every refusal in a package, one at a time, and
// says which ones nothing noticed.
//
// # WHY THIS EXISTS
//
// `mutate` proves that one test can fail. This asks the same question of a
// whole package: of every guard that refuses something, which ones can be
// deleted with the suite staying green? Over five runs on go-crdt it put 574
// guards through that and found thirteen that no test held — including a
// decoder that panics on a peer's bytes once its bound is gone.
//
// It was a Python script first. It is Go now because the workshop's tools are
// Go, and because three of its lessons are worth holding in types rather than
// in care:
//
//	a mutant that does not COMPILE is not a mutant. Counting it as killed
//	flatters the suite; counting it as survived slanders it. It is a third
//	verdict, and on a real package it is often HALF of everything tried —
//	242 of 574.
//
//	a mutant can HANG. Deleting the check that a retry policy is honourable
//	does not fail a suite, it makes the suite wait out an hour somebody
//	mistyped. That is a fourth verdict: the guard is load-bearing, and
//	nothing says so. Without a timeout the sweep itself dies there.
//
//	the file must come back. Every mutation is applied by `mutate`, which
//	restores on its own exit and on a signal, so a sweep interrupted
//	half-way leaves no deliberate defect behind.
//
// # WHAT IT DOES
//
//	mutsweep [-dir .] [-files a.go,b.go] [-timeout 5m] -- go test ./...
//
// It walks the syntax tree for an `if` whose body returns a refusal, deletes
// each one in turn through `mutate`, and prints a line per guard and a summary.
// It exits non-zero when anything survived or hung, because those are the two
// answers that need somebody to read them.
package main

import (
	"fmt"
	"io"
	"os"
)

// active marks a sweep in progress, in the environment the command inherits.
//
// A sweep runs a command of somebody's choosing, and if that command reaches
// this tool again the second sweep runs the command again, and so on. It is not
// hypothetical: a config built without a run directory once made the command
// run in THIS tool's own directory, where `go test ./...` is this tool's own
// suite -- 10,837 processes and a load average of 532 before it was noticed.
//
// A path check would be too narrow: the recursion is about the command, not
// about a directory. This refuses the second sweep whatever it was asked to do.
const active = "MUTSWEEP_ACTIVE"

func main() {
	if os.Getenv(active) != "" {
		fmt.Fprintln(os.Stderr, "mutsweep: refusing to run inside another sweep — "+
			"the command a sweep runs must not reach this tool, or each mutant starts a sweep of its own")
		os.Exit(2)
	}
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(argv []string, stdout, stderr io.Writer) int {
	cfg, cmd, code := parse(argv, stderr)
	if code != 0 {
		return code
	}
	guards, err := collect(cfg)
	if err != nil {
		fmt.Fprintln(stderr, "mutsweep:", err)
		return 2
	}
	if len(guards) == 0 {
		// A sweep that found nothing to do is not a clean sweep. A changed
		// predicate, a wrong directory or a build tag that excludes everything
		// all arrive here, and all of them read as success if this returns 0.
		fmt.Fprintln(stderr, "mutsweep: no refusal found to delete — the walk is wrong, not the package")
		return 2
	}
	return sweep(cfg, guards, cmd, stdout, stderr)
}
