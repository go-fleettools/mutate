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
