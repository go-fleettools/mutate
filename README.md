# mutate

![Go](https://img.shields.io/badge/Go-1.27%2B-00ADD8?logo=go&logoColor=white)
![License](https://img.shields.io/badge/license-BSD--3--Clause-blue)

**Prove that a test can fail.** Apply one exact source mutation, run the
suite, restore whatever happens — and exit 0 only when the suite went
**red**.

```console
$ mutate -file browse.go -name "mark repeats too" \
    -from 'if !n.Repeat {' -to 'if true {' \
    -- go test -run TestARepeatIsNot ./...
mutate: mark repeats too — caught
```

```sh
go install github.com/go-fleettools/mutate@latest          # one mutation
go install github.com/go-fleettools/mutate/mutsweep@latest # every refusal in a package
```

No dependencies beyond the standard library.

## `mutsweep`: the same question, of a whole package

`mutate` proves one test can fail. `mutsweep` asks it of every refusal in a
package: delete each one in turn, and say which ones nothing noticed.

```console
$ mutsweep -dir structured -files blob.go,cell.go -run-in . -- go test ./...
mutsweep: 38 refusals, 3m0s each at most, through "go test ./..."
[1/38] caught       blob.go:366  (size == 0) != (count == 0)  5s
[15/38] SURVIVED    blob.go:347  used <= 0  12s
...

38 refusals: 15 caught, 21 not mutants, 2 survived, 0 hung

what nothing holds:
  SURVIVED   blob.go:347  used <= 0
  SURVIVED   cell.go:98   !ok
```

```sh
go install github.com/go-fleettools/mutate/mutsweep@latest
```

Over five runs on one project it put **574 guards** through that and found
**thirteen** no test held — including a decoder that panics on a peer's
bytes once its bound is gone.

### Answering a report: `-only`

A sweep is the slow half. The half that happens over and over is the other one:
the report names survivors, tests get written for them, and the question becomes
whether those tests *kill* them — which is not the same question as whether they
pass. A test can be green because it holds the guard, or green because it never
reaches it.

```console
$ mutsweep -only map.go:163,map.go:628,map.go:856,map.go:861,map.go:1002 \
      -timeout 2m -- go test -count 1 ./...
mutsweep: 5 refusals, 2m0s each at most, through "go test -count 1 ./..."
[1/5] caught       map.go:163  op.Kind != MapSet && op.Kind != MapDelete && op.Kind != Map…  15s
[2/5] HUNG         map.go:628  last-had > uint64(len(m.pending))  no answer in 2m0s
[3/5] caught       map.go:856  !ok || below > MaxClock  14s
[4/5] SURVIVED     map.go:861  !ok  13s
[5/5] SURVIVED     map.go:1002  len(b) == 0  14s

5 refusals: 2 caught, 0 not mutants, 2 survived, 1 hung
```

That is a real run, and it is the answer to a report: those five lines were
survivors of a 204-refusal sweep that took half an hour, tests were written for
three of them, and this re-asked the question in three minutes. It says the two
tests land, that the two left alone are still unheld — on purpose, each being the
same value by another line — and that one guard still has no answer at all.

Targets are spelled exactly as the report prints them, and `-only` reads only the
files it was given. The cost is one run of the command per target — about fifteen
seconds each here, plus whatever a `HUNG` one waits out — against a whole sweep's
half hour.

**A target that matches no refusal is an error naming the target.** An `-only`
list is written from a report and goes stale two ways — the file is edited after
the report, or the line is mistyped — and both otherwise end as a sweep of
nothing, which is the one result that reads like a clean one.

### A file the build excludes is not a file nothing covers

This is the one way a sweep can report a clean package it never compiled, and it
is silent.

A `//go:build js && wasm` file is not in a native build. The command does not
compile it, so deleting a refusal in it changes nothing the command can see, the
suite passes, and the verdict is **SURVIVED — nothing covers this**, when the
truth is that nothing *looked*. Measured on `go-crdt/collab`, whose root holds
286 refusals of which **41** are under that constraint: one of them deleted gave
`THE SUITE SURVIVED IT` after ninety-three seconds of running a suite that never
compiled the file.

So `mutsweep` reads `GOOS` and `GOARCH` the way the go tool does and asks the
same question about each file. **Naming a file is a claim about it**, so an
excluded one named in `-files` or `-only` is an error, with exit status 2:

```console
$ mutsweep -files peer_js.go -- go test ./...
mutsweep: the darwin/arm64 build excludes peer_js.go — a refusal there is not
compiled by the command, so deleting it changes nothing and every one would be
reported as SURVIVED when nothing looked. Sweep them with the platform set on BOTH
this command and the one it runs, and with a command that RUNS the tests rather
than one that only builds them ...
```

A directory sweep is not a claim about every file in it, so those are skipped —
and **named**, because skipping them quietly is how a campaign comes to believe it
covered a package it never compiled:

```console
$ mutsweep -dir . -- go test ./...
mutsweep: not swept, the darwin/arm64 build excludes them: bcast_js.go,
bcast_lock_js.go, peer_js.go, webrtc_js.go, websocket_js.go
mutsweep: 245 refusals, 5m0s each at most, through "go test ./..."
```

286 before, 245 after, and the 41 accounted for by name rather than by a number
nobody can check.

The advice in that message names a command that **runs** the tests, and the reason
is worth repeating: its first draft suggested `GOOS=js GOARCH=wasm go vet`, which
compiles the file — so the mutant is a real mutant — but cannot go red for any
mutation that still compiles. Every refusal came back `SURVIVED` in one second. A
sweep needs a command that *can* fail.

### Four verdicts, because two is not enough

| verdict | meaning |
| --- | --- |
| **caught** | the suite went red: the guard is held |
| **SURVIVED** | the suite stayed green: nothing holds it |
| **not a mutant** | the deletion did not compile. On real code this is often **half** of everything tried — 242 of 574 — and counting it either way is wrong |
| **HUNG** | no answer in time. The guard is load-bearing *and* nothing says so: a timeout names no cause and costs a runner its whole budget |

`HUNG` is not a theoretical category. Deleting the check that a retry policy
is honourable does not fail a suite; it makes the suite wait out an hour
somebody mistyped into the wrong field.

### The taxonomy is not invented here

PIT, the mutation testing tool for Java, has reported these outcomes for years,
and four of them are the four above under other names
([pitest.org, *Basic concepts*](https://pitest.org/quickstart/basic_concepts)):

| PIT | here | PIT's definition |
| --- | --- | --- |
| Killed | caught | "A test caught the mutation successfully." |
| Survived | SURVIVED | "The mutation was not detected by the covering test." |
| Timed Out | HUNG | "A mutation may time out if it causes an infinite loop, such as removing the increment from a counter in a for loop." |
| Non viable | not a mutant | "could not be loaded by the JVM as the bytecode was in some way invalid" |
| Run error | NOT RUN | "something went wrong when trying to test the mutation" |

Arriving at the same five independently is some evidence they are the joints of
the thing rather than this tool's habits. PIT has two more that this does not:
**No coverage**, which it can report because it knows which tests cover each
line, and **Memory error**. Neither is available here, and a sweep that cannot
tell "no test covers this line" from "the tests covered it and said nothing"
should not pretend otherwise — in Go both arrive as a green suite.

Whether PIT counts a timed-out mutant as killed in its score, that page does not
say. Here it is kept apart deliberately: a suite that notices only by failing to
return names no cause, costs a runner its whole budget, and on a loaded machine
is indistinguishable from slowness.

### What it refuses to do

**Run inside another sweep.** A sweep runs a command of somebody's choosing,
and if that command reaches this tool again, each mutant starts a sweep of
its own. That is not hypothetical: a configuration with no run directory
once made the command run in the tool's own directory, where `go test ./...`
is the tool's own suite — **10,837 processes and a load average of 532**
before it was noticed. The refusal travels in the environment, because the
recursion is about the *command*, not about a directory.

**Report a guard it could not address.** Two guards in one file often have
identical text, and `mutate` rightly refuses an ambiguous replacement. Each
deletion is widened with preceding source until it occurs exactly once —
without which those guards read as "not a mutant", and a sweep validated
against a known answer showed it had been dropping real survivors in
silence.

**Pass a sweep that found nothing.** A changed predicate, a wrong directory
and a build tag that excludes everything all arrive at "no refusal found",
and all of them read as success unless that is an error.

### A survivor is not yet a defect

Of 66 survivors across those five runs, 57 were not defects: a bound doubled
one layer down, a fast path whose general path gives the same answer, a
short-circuit that only saves work. Read each one — and read the **end** of
the function, since most of them are held by something further in. The
summary says so where somebody will see it.

## Why it is a program and not a one-liner

A green test says nothing on its own. It says something once you have seen
it go red for the reason it claims to guard — and the only way to see that
is to break the code on purpose and watch.

That had been a throwaway `perl -0pi -e` per mutation, about **twenty-five
times in one session**: an ad-hoc regex, an ad-hoc restore, and no record
afterwards of which mutations were actually tried. Three things went wrong
in those twenty-five. Each is now impossible rather than a matter of care.

| what went wrong | what it looked like | what happens now |
| --- | --- | --- |
| the regex matched nothing | the "mutation" ran against **unchanged code**, the test passed, and that read as the test being weak | exact text, never a pattern; **zero matches is refused** before anything runs |
| the mutant did not compile | not a failing test — **no test at all**, and identical to a real failure in a `grep FAIL` | build failures are told apart and reported as *"proves nothing"* |
| the restore was forgotten | a deliberate defect left in a tree, carried by the next commit | restored whatever happens, **including on SIGINT** |

A replacement matching **twice** is refused too: replacing both is a larger
mutation than the one being described, and the report would name the small
one.

## The verdict is the point

| exit | meaning |
| --- | --- |
| **0** | the command failed → the mutation was **caught**; something covers this |
| **1** | the command passed → `THE SUITE SURVIVED IT. Nothing covers this.` |
| **2** | refused (no match, ambiguous match) or the mutant did not compile |

A hole is reported in those words rather than as a bare non-zero exit,
because the whole value of running this is learning which of the three you
are looking at.

`-expect-pass` inverts it, for a change that must **not** matter — a comment
reflowed, a constant renamed. A suite that goes red on one of those is
pinning its own shape rather than a behaviour.

`-name` labels the mutation for the report; without it the first line of the
replaced text is used.

## It is pointed at itself

A verifier nobody verifies is the thing it exists to prevent. Unit tests
cover the no-op mutation, the ambiguous one, the caught one, the
**surviving** one, restoration after success / failure / a command that does
not exist, the build failure, and both directions of `-expect-pass`.

CI adds an end-to-end selfcheck through the built binary, which a unit test
cannot replace:

| arm | expected |
| --- | --- |
| break the ambiguity guard | the test covering it goes red → **exit 0** |
| reword a comment | nothing goes red → **exit 1**, saying the suite survived |
| then `git diff --exit-code` | the file is back |

Three operating systems, because this tool edits files, spawns processes
and traps signals — and the first version of one test shelled out to a
`.sh`, which Windows cannot execute, so the command failed to *start* and
that read as the command failing.

## What it does not do

It does not **generate** mutations. Frameworks that do produce thousands and
leave you to judge them; this takes the one you can already name — the line
you suspect is unguarded — and answers that single question in a second.

It does not know Go. `-from`/`-to` are exact text and the command is
anything that exits non-zero on failure, so it works on any language whose
tests you can run from a shell.

BSD-3-Clause.
