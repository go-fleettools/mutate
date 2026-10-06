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
go install github.com/go-fleettools/mutate@latest
```

No dependencies beyond the standard library.

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
