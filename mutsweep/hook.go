package main

// # THE PUSH GATE
//
//	mutsweep -hook      (a Claude Code PreToolUse hook on Bash)
//
// reads the hook payload, and when the command pushes with `gitpush`, refuses
// the push if it SENDS a non-test Go file holding refusals that no recorded
// sweep has covered at exactly those bytes -- or that a sweep left SURVIVED or
// HUNG. Everything else is allowed without a word, and cheaply: the hook runs
// before every Bash call, and a command that does not push costs one JSON decode
// and a scan of its text.
//
// ⛔ WHY A HOOK AND NOT ANOTHER NOTE. This tool was a gate that nothing called.
// The lesson it exists for (a-break-check-that-did-not-break) recorded its
// SEVENTH occurrence on 2026-11-19 with mutsweep already installed and already
// named as the lesson's gate: two sessions hand-mutated, wrote "break-checked",
// and never ran it. A tool one has to remember to run is a note with a binary
// attached. The user chose this refusal on 2026-11-19 over "warn only" and over
// "do nothing" -- and "warn only" is what cmd/guards did, whose README said "an
// analysis tool, not a gate: nothing fails when it is not run".
//
// # WHAT IT JUDGES, AND WHAT IT DOES NOT
//
// It judges the files changed between HEAD and its merge base with the remote's
// default branch, at the bytes COMMITTED at HEAD -- what the push sends, not the
// working tree, so an uncommitted edit cannot decide whether a commit is
// covered. Test files, testdata/ and vendor/ are not judged. A file the native
// build excludes is not judged either, because a native sweep could not have
// judged it (guards.go's excludedError says why).
//
// Three outcomes per file, and only the first two refuse:
//
//	no record for these bytes   refused: never swept as it is now
//	a record with SURVIVED/HUNG refused: a sweep found refusals nothing holds
//	caught by PANIC only         allowed, and said: for an INDEXING guard
//	                             (deleting it makes the next line index out of
//	                             range) PANIC is the only red any test can
//	                             produce, so refusing it would make the gate
//	                             impassable for correct code -- and a gate that
//	                             must always be waived teaches waiving.
//
// ⚠ It judges what HEAD is, so `gitpush origin other-branch` from a checkout of
// a different branch is judged by the wrong commit. ⚠ It does not see `ghmerge`,
// which merges by number with no tree here to hash; a branch merged that way
// was pushed, and the push is where it was judged. ⚠ A record is keyed by the
// guarded file only (record.go says why): deleting the test that held a guard
// does not invalidate the record.
//
// # WHEN IT CANNOT READ
//
// It ALLOWS, and SAYS SO to the user through the hook's systemMessage. About
// twenty-five sessions share this machine, and a refusal it cannot justify is
// not a verdict -- but an allowance it does not announce would be the
// gate-that-does-nothing again. "Not judged" is printed every time it happens.
//
// # THE WAIVER
//
//	MUTSWEEP_SKIP=1 gitpush ...
//
// allows the push and prints, to the user, which files went out unswept. A
// waiver is made in public or not at all.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// skipEnv is the waiver's variable, as a command prefix.
const skipEnv = "MUTSWEEP_SKIP"

type hookPayload struct {
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
	Cwd string `json:"cwd"`
}

// push is one `gitpush` found in a command, and where it would run.
type push struct {
	dir  string
	skip bool
	// moved names a git command earlier in the same line that moves HEAD
	// (`git commit`, …). The hook runs BEFORE the line, so it would judge the
	// HEAD from before that commit: an empty diff, allowed in silence. #26.
	moved string
}

// headMovers are the git subcommands after which HEAD is not what the hook read.
var headMovers = map[string]bool{
	"commit": true, "merge": true, "cherry-pick": true, "rebase": true, "am": true,
	"revert": true, "reset": true, "checkout": true, "switch": true, "pull": true,
}

// headMover returns the subcommand when the words are a git invocation that
// moves HEAD, skipping git's own leading options (-C dir, -c key=value, --flags).
func headMover(f []string) string {
	if len(f) == 0 || filepath.Base(f[0]) != "git" {
		return ""
	}
	for i := 1; i < len(f); i++ {
		switch {
		case f[i] == "-C" || f[i] == "-c":
			i++ // the option's value
		case strings.HasPrefix(f[i], "-"):
		default:
			if headMovers[f[i]] {
				return f[i]
			}
			return ""
		}
	}
	return ""
}

// pushesIn finds the gitpush invocations in a Bash command line.
//
// The command is split on && || ; | and newlines, and a `cd` earlier in the
// chain moves where later segments run, because `cd <clone> && gitpush ...` is
// how nearly every push on this machine is written. Leading VAR=value words are
// skipped, and MUTSWEEP_SKIP=1 among them is the waiver.
//
// ⚠ This is a reading of the text, not a shell: a push hidden inside a quoted
// string or an `eval` is not seen, and is therefore ALLOWED. That is the
// direction this hook fails in whenever it cannot read.
func pushesIn(command, cwd string) []push {
	dir := cwd
	moved := ""
	var out []push
	for _, seg := range splitSegments(command) {
		f := strings.Fields(seg)
		skip := false
		for len(f) > 0 && isAssignment(f[0]) {
			if f[0] == skipEnv+"=1" {
				skip = true
			}
			f = f[1:]
		}
		if len(f) == 0 {
			continue
		}
		switch {
		case f[0] == "cd" && len(f) > 1:
			dir = resolveDir(dir, f[1])
		case filepath.Base(f[0]) == "gitpush":
			out = append(out, push{dir: dir, skip: skip, moved: moved})
		default:
			if m := headMover(f); m != "" && moved == "" {
				moved = "git " + m
			}
		}
	}
	return out
}

// splitSegments cuts a command at its control operators. The two-character
// operators go first, so `||` is not read as two pipes.
func splitSegments(command string) []string {
	r := strings.NewReplacer("&&", "\x00", "||", "\x00", ";", "\x00", "|", "\x00", "\n", "\x00")
	return strings.Split(r.Replace(command), "\x00")
}

// isAssignment reports whether a word is a shell variable assignment prefix.
func isAssignment(w string) bool {
	name, _, ok := strings.Cut(w, "=")
	if !ok || name == "" {
		return false
	}
	for i, c := range name {
		letter := c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if !letter && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// resolveDir is where `cd arg` leaves a shell that was in dir.
func resolveDir(dir, arg string) string {
	arg = strings.Trim(arg, `"'`)
	if arg == "~" || strings.HasPrefix(arg, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			arg = filepath.Join(home, strings.TrimPrefix(arg, "~"))
		}
	}
	// A leading slash is absolute in the SHELL this command was written for,
	// whatever filepath thinks: on Windows `/b` has no drive and IsAbs says
	// no, so `cd /b` was read as a subdirectory of where the shell already was.
	// Windows CI found it.
	if !filepath.IsAbs(arg) && !strings.HasPrefix(arg, "/") {
		arg = filepath.Join(dir, arg)
	}
	return filepath.Clean(arg)
}

// gitOut runs git in dir and returns its standard output UNMODIFIED: the blob
// `git show` prints is hashed, and a trimmed blob is a different file.
//
// It is a variable so a test can make ONE git step fail: the diff that follows a
// merge-base that worked, or a file the diff just listed that HEAD does not
// hold, cannot be arranged in a real repository.
var gitOut = func(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errBuf.String()))
	}
	return out, nil
}

// verdictOf is what the gate concluded about one pushed repository.
type verdictOf struct {
	root     string
	refused  map[string][]string // repo-relative file -> why
	only     map[string][]string // repo-relative file -> the -only targets that clear it
	panics   []string            // allowed, said
	unjudged []string            // could not read: allowed, said
}

// judge reads what a push from dir would send and decides each file.
func judge(dir string) (verdictOf, error) {
	v := verdictOf{refused: map[string][]string{}, only: map[string][]string{}}
	top, err := gitOut(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return v, err
	}
	v.root = strings.TrimSpace(string(top))
	base := ""
	for _, ref := range []string{"origin/HEAD", "origin/main", "origin/master"} {
		if out, err := gitOut(v.root, "merge-base", "HEAD", ref); err == nil {
			base = strings.TrimSpace(string(out))
			break
		}
	}
	if base == "" {
		return v, fmt.Errorf("no origin/HEAD, origin/main or origin/master in %s to compare HEAD with", v.root)
	}
	list, err := gitOut(v.root, "diff", "--name-only", "--diff-filter=AMR", base, "HEAD", "--", "*.go")
	if err != nil {
		return v, err
	}
	for _, f := range strings.Split(strings.TrimSpace(string(list)), "\n") {
		if f == "" || strings.HasSuffix(f, "_test.go") || underSkippedDir(f) {
			continue
		}
		judgeFile(&v, base, f)
	}
	return v, nil
}

// changedLines is the set of HEAD-side lines of f that the push adds or
// rewrites against base: the new side of each hunk of a zero-context diff.
//
// ⚠ A guard is judged where its condition STARTS (guard.line). A change inside
// the body of an if, leaving its condition alone, does not make that guard
// "changed": what the record vouches for is the refusal, and its deletion is
// the condition, so the condition's line is the one that has to be in the diff.
func changedLines(root, base, f string) (map[int]bool, error) {
	out, err := gitOut(root, "diff", "-U0", "--no-color", "--no-ext-diff", base, "HEAD", "--", f)
	if err != nil {
		return nil, err
	}
	lines := map[int]bool{}
	for _, l := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(l, "@@ ") {
			continue
		}
		start, count, err := newSide(l)
		if err != nil {
			return nil, err
		}
		for i := start; i < start+count; i++ {
			lines[i] = true
		}
	}
	return lines, nil
}

// newSide reads "+c,d" (or "+c", meaning one line) from a hunk header
// "@@ -a,b +c,d @@ …". d is 0 for a pure deletion, which changes no HEAD line.
func newSide(header string) (start, count int, err error) {
	fields := strings.Fields(header)
	if len(fields) < 3 || !strings.HasPrefix(fields[2], "+") {
		return 0, 0, fmt.Errorf("not a hunk header: %q", header)
	}
	spec := strings.TrimPrefix(fields[2], "+")
	count = 1
	if c, n, ok := strings.Cut(spec, ","); ok {
		spec = c
		if count, err = strconv.Atoi(n); err != nil {
			return 0, 0, fmt.Errorf("hunk header %q: %v", header, err)
		}
	}
	if start, err = strconv.Atoi(spec); err != nil {
		return 0, 0, fmt.Errorf("hunk header %q: %v", header, err)
	}
	return start, count, nil
}

// underSkippedDir reports a path under testdata/ or vendor/, which the go tool
// itself does not build as part of the package.
func underSkippedDir(f string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(f), "/") {
		if seg == "testdata" || seg == "vendor" {
			return true
		}
	}
	return false
}

// judgeFile decides one pushed file, on the refusals the push CHANGED (#24): a
// guard whose condition line is in the diff against base. The others were there
// before this push and are not its to answer for -- they are judged when their
// own lines change, and a full sweep remains the way to audit them all.
func judgeFile(v *verdictOf, base, f string) {
	src, err := gitOut(v.root, "show", "HEAD:"+f)
	if err != nil {
		v.unjudged = append(v.unjudged, f+": "+err.Error())
		return
	}
	d, name := filepath.Join(v.root, filepath.Dir(f)), filepath.Base(f)
	in, _, err := partitionByBuild(d, []string{name})
	if err != nil {
		v.unjudged = append(v.unjudged, f+": "+err.Error())
		return
	}
	if len(in) == 0 {
		return // the native build leaves it out, so no native sweep could judge it
	}
	gs, err := guardsIn(d, name, src, false)
	if err != nil {
		v.unjudged = append(v.unjudged, f+": "+err.Error())
		return
	}
	changed, err := changedLines(v.root, base, f)
	if err != nil {
		v.unjudged = append(v.unjudged, f+": "+err.Error())
		return
	}
	var mine []guard
	for _, g := range gs {
		if changed[g.line] {
			mine = append(mine, g)
		}
	}
	if len(mine) == 0 {
		return // the push changed no refusal here: nothing of its own to cover
	}
	rec, ok, err := lookupRecord(contentKey(src))
	if err != nil {
		// Refused, not allowed: a record that cannot be read is one a re-sweep
		// replaces, and allowing on it would let a corrupt file vouch.
		v.refused[f] = append(v.refused[f], "its record cannot be read: "+err.Error())
		return
	}
	var unswept []string
	for _, g := range mine {
		if !ok || !rec.covers(g.line, len(gs)) {
			unswept = append(unswept, fmt.Sprintf("%s:%d", name, g.line))
		}
	}
	if len(unswept) > 0 {
		v.refused[f] = append(v.refused[f], fmt.Sprintf("%d changed refusal(s) never swept as committed: %s",
			len(unswept), strings.Join(unswept, ",")))
		v.only[f] = unswept
		return
	}
	for _, e := range rec.Needed {
		if !changed[e.Line] {
			continue // a verdict on a refusal this push did not touch
		}
		switch e.Verdict {
		case survived, hung:
			v.refused[f] = append(v.refused[f], fmt.Sprintf("%s %s:%d  %s", e.Verdict, name, e.Line, e.Cond))
		case caughtByPanic:
			v.panics = append(v.panics, fmt.Sprintf("%s:%d", f, e.Line))
		}
	}
}

// moduleRoot is the nearest directory at or above dir holding a go.mod, stopping
// at the repository root.
func moduleRoot(dir, root string) string {
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		if d == root || d == filepath.Dir(d) {
			return root
		}
	}
}

// relPath is filepath.Rel behind a name: on Windows it fails across volumes, and
// no path a POSIX test can build makes it fail here.
var relPath = filepath.Rel

// remedy is the command that clears a refusal, per package directory.
func remedy(v verdictOf) []string {
	byDir := map[string][]string{}
	for f := range v.refused {
		d := filepath.Dir(f)
		byDir[d] = append(byDir[d], filepath.Base(f))
	}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	var out []string
	for _, d := range dirs {
		abs := filepath.Join(v.root, d)
		mod := moduleRoot(abs, v.root)
		rel, err := relPath(mod, abs)
		if err != nil {
			rel = abs
		}
		names := byDir[d]
		sort.Strings(names)
		// Where every refused file names its unswept lines, ask about those
		// lines alone: that is the whole of what the push owes (#24). Any other
		// refusal here (a record that cannot be read) needs the full sweep.
		var targets []string
		for _, n := range names {
			t := v.only[filepath.Join(d, n)]
			if len(t) == 0 {
				targets = nil
				break
			}
			targets = append(targets, t...)
		}
		if len(targets) > 0 {
			out = append(out, fmt.Sprintf("cd %s && mutsweep -dir %s -run-in . -only %s -- go test ./...",
				mod, rel, strings.Join(targets, ",")))
			continue
		}
		out = append(out, fmt.Sprintf("cd %s && mutsweep -dir %s -run-in . -files %s -- go test ./...",
			mod, rel, strings.Join(names, ",")))
	}
	return out
}

// systemMessage prints text the user sees, and allows.
func systemMessage(stdout io.Writer, text string) int {
	b, _ := json.Marshal(map[string]string{"systemMessage": text})
	fmt.Fprintln(stdout, string(b))
	return 0
}

// hookMain answers one PreToolUse payload. 0 allows; 2 refuses, with the reason
// on stderr, which Claude Code shows to the model.
func hookMain(in io.Reader, stdout, stderr io.Writer) int {
	var p hookPayload
	if err := json.NewDecoder(in).Decode(&p); err != nil {
		return systemMessage(stdout, "mutsweep gate: NOT JUDGED, the hook payload could not be read ("+
			err.Error()+") — allowed without a reading")
	}
	if p.ToolName != "" && p.ToolName != "Bash" {
		return 0
	}
	// No push in the command: the loop below has nothing to judge, and the
	// answer is the plain 0 at the end.
	pushes := pushesIn(p.ToolInput.Command, p.Cwd)
	var refusal, waived, said []string
	for _, ps := range pushes {
		if ps.moved != "" {
			// Refused, not allowed: this is not a surface the hook cannot read but
			// one it would read WRONG — the HEAD it sees is not the one pushed —
			// and the remedy is free and certain.
			if ps.skip {
				waived = append(waived, "(a push after `"+ps.moved+"` in the same command, not judged)")
				continue
			}
			refusal = append(refusal, "in "+ps.dir+":",
				"  this command runs `"+ps.moved+"` before it pushes, and the gate runs BEFORE the command:",
				"  it would judge HEAD as it is now, not the commit being pushed (#26).",
				"", "Run the push as a command of its own, after the commit:",
				"  gitpush …")
			continue
		}
		v, err := judge(ps.dir)
		if err != nil {
			said = append(said, "NOT JUDGED in "+ps.dir+": "+err.Error())
			continue
		}
		for _, u := range v.unjudged {
			said = append(said, "NOT JUDGED "+u)
		}
		if len(v.refused) == 0 {
			continue
		}
		files := make([]string, 0, len(v.refused))
		for f := range v.refused {
			files = append(files, f)
		}
		sort.Strings(files)
		if ps.skip {
			waived = append(waived, files...)
			continue
		}
		refusal = append(refusal, "in "+v.root+":")
		for _, f := range files {
			for _, why := range v.refused[f] {
				refusal = append(refusal, "  "+f+"  "+why)
			}
		}
		refusal = append(refusal, "", "Sweep them, from the module root:")
		for _, c := range remedy(v) {
			refusal = append(refusal, "  "+c)
		}
		if len(v.panics) > 0 {
			refusal = append(refusal, "", "(caught by PANIC, NOT refused — the ceiling for an indexing guard: "+
				strings.Join(v.panics, ", ")+")")
		}
	}
	if len(refusal) > 0 {
		fmt.Fprintln(stderr, "⛔ mutsweep gate: this push is refused — what it sends has not been judged covered.")
		fmt.Fprintln(stderr)
		for _, l := range refusal {
			fmt.Fprintln(stderr, l)
		}
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "A SURVIVED guard needs a test that fails without it; then re-ask only those")
		fmt.Fprintln(stderr, "lines with `mutsweep -only file.go:line ...`, which updates the record.")
		fmt.Fprintln(stderr, "A deliberate exception is `"+skipEnv+"=1 gitpush ...`, and it is shown to the user.")
		for _, s := range said {
			fmt.Fprintln(stderr, s)
		}
		return 2
	}
	var msg []string
	if len(waived) > 0 {
		msg = append(msg, "mutsweep gate WAIVED by "+skipEnv+"=1 — pushed unswept: "+strings.Join(waived, ", "))
	}
	msg = append(msg, said...)
	if len(msg) > 0 {
		return systemMessage(stdout, strings.Join(msg, "\n"))
	}
	return 0
}
