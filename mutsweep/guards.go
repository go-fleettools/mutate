package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type config struct {
	dir     string // where the files to mutate are
	runIn   string // where the command runs; often the module root above dir
	files   []string
	only    []target
	timeout time.Duration
	mutate  string
	tally   bool   // count per file and stop, running nothing
	mode    string // what to do to each refusal: modeDelete (the default) or modeBoundary
	sites   string // which sites to visit: sitesRefusals (the default) or sitesAll
}

const (
	modeDelete   = "delete"
	modeBoundary = "boundary"

	sitesRefusals = "refusals"
	sitesAll      = "all"
)

func (c config) boundary() bool { return c.mode == modeBoundary }

// allSites is whether -sites all was asked for (#21).
func (c config) allSites() bool { return c.sites == sitesAll }

// noun is what a sweep counts: a refusal is deleted, a comparison is moved.
func (c config) noun() string {
	if c.boundary() {
		return "comparisons"
	}
	return "refusals"
}

// flipped is the boundary mutation of a comparison operator: the same test one
// step to the other side of the edge. Equality is not a boundary.
var flipped = map[token.Token]string{
	token.LSS: "<=", token.LEQ: "<", token.GTR: ">=", token.GEQ: ">",
}

// target names one refusal the way a report prints it: the file as -files
// spells it, and the line the guard starts on.
type target struct {
	file string
	line int
}

func (t target) String() string { return fmt.Sprintf("%s:%d", t.file, t.line) }

// guard is one `if` that refuses something: the exact bytes to delete, and
// enough to name it in a report.
type guard struct {
	dir  string // the -dir it was found in
	file string // its name within that directory
	line int
	cond string
	// from and to are what mutate replaces. from is the guard, widened with
	// preceding source until it occurs exactly once in the file, because two
	// guards in one file often have IDENTICAL text -- `if used <= 0 { return
	// ..., false }` twice in a decoder is ordinary -- and mutate rightly
	// refuses an ambiguous replacement. Without the widening those guards are
	// reported as "not a mutant", which is wrong: they are mutable, they are
	// just not addressable by their own text. Validating against a sweep whose
	// answer was already known is what surfaced it; it had silently dropped two
	// real survivors.
	from, to string
	// altFrom and altTo are the fallback mutation, used when deleting the `if`
	// does not compile (a variable used only in its condition is left unused):
	// the condition replaced by `false && (C)`. Empty when from/to already is it.
	altFrom, altTo string
	// kind is "" for the refusals every sweep visits, and for the sites of
	// -sites all (#21) one of loop, branch, return=true, return=false.
	kind string
}

func parse(argv []string, stderr io.Writer) (config, []string, int) {
	fs := flag.NewFlagSet("mutsweep", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cfg config
	fs.StringVar(&cfg.dir, "dir", ".", "the directory holding the files to sweep")
	fs.StringVar(&cfg.runIn, "run-in", "", "where to run the command; default is -dir.\n"+
		"Give the MODULE ROOT when sweeping a subpackage: `go test ./...` run inside the\n"+
		"subpackage cannot see a test that lives above it, and a guard those tests hold\n"+
		"would be reported as surviving")
	files := fs.String("files", "", "comma-separated files to sweep; default is every non-test .go in -dir")
	only := fs.String("only", "", "comma-separated `file.go:line` refusals to sweep, spelled as a report prints\n"+
		"them. For re-running exactly the survivors of an earlier sweep once tests have\n"+
		"been written for them: one run of the command per target, rather than one per\n"+
		"refusal in the package. A target matching no refusal is an error, because a\n"+
		"sweep that quietly swept nothing reads as a clean one")
	fs.DurationVar(&cfg.timeout, "timeout", 5*time.Minute, "how long one mutant may run before it is called HUNG")
	fs.StringVar(&cfg.mutate, "mutate-bin", "mutate", "the mutate command to drive")
	fs.StringVar(&cfg.mode, "mutate", modeDelete, "what to do to each refusal: `delete` it (the default), or\n"+
		"`boundary`: move each comparison in a refusal's condition, and in a for\n"+
		"loop's, to the other side of its edge (< to <=, <= to <, > to >=, >= to >).\n"+
		"Deleting a refusal is the wrong mutation for a range guard, whose defects are\n"+
		"off-by-one ones")
	fs.StringVar(&cfg.sites, "sites", sitesRefusals, "which sites to visit: `refusals` (the default: an if whose body ends in a\n"+
		"return, with or without an init clause or an else), or `all`, which adds\n"+
		"[loop] an if whose body ends in continue or break, [branch] every other if\n"+
		"(its condition becomes false && (C)), and [return=true] / [return=false] a\n"+
		"boolean return expression. Off by default because more sites change the count\n"+
		"a record is judged against; -tally lists what a default sweep leaves out")
	fs.BoolVar(&cfg.tally, "tally", false, "count the refusals per FILE and stop, running nothing.\n"+
		"For planning a campaign in chunks, and for checking afterwards that the chunks\n"+
		"covered the package: no command is needed with it")
	if err := fs.Parse(argv); err != nil {
		return cfg, nil, 2
	}
	if cfg.sites != sitesRefusals && cfg.sites != sitesAll {
		fmt.Fprintf(stderr, "mutsweep: -sites %q: it is %s or %s\n", cfg.sites, sitesRefusals, sitesAll)
		return cfg, nil, 2
	}
	if cfg.mode != modeDelete && cfg.mode != modeBoundary {
		fmt.Fprintf(stderr, "mutsweep: -mutate %q: it is %s or %s (the mutate COMMAND is now -mutate-bin)\n",
			cfg.mode, modeDelete, modeBoundary)
		return cfg, nil, 2
	}
	if *files != "" {
		cfg.files = strings.Split(*files, ",")
	}
	if *only != "" {
		var err error
		if cfg.only, err = parseTargets(*only); err != nil {
			fmt.Fprintln(stderr, "mutsweep:", err)
			return cfg, nil, 2
		}
	}
	if cfg.runIn == "" {
		cfg.runIn = cfg.dir
	}
	cmd := fs.Args()
	if len(cmd) == 0 && !cfg.tally {
		// -tally runs nothing, so it needs nothing to run. Every other mode
		// does, and a sweep with no command would report every guard as
		// surviving a suite that was never there.
		fmt.Fprintln(stderr, "usage: mutsweep [-dir D] [-files a.go,b.go] [-only a.go:12,...] [-timeout 5m] -- command...")
		fmt.Fprintln(stderr, "       mutsweep [-dir D] [-files a.go,b.go] -tally")
		return cfg, nil, 2
	}
	return cfg, cmd, 0
}

// parseTargets reads the -only list. A target it cannot read is an error here
// rather than a target that matches nothing later, so the message names the
// spelling rather than the consequence.
func parseTargets(s string) ([]target, error) {
	var out []target
	for _, field := range strings.Split(s, ",") {
		field = strings.TrimSpace(field)
		at := strings.LastIndexByte(field, ':')
		if at <= 0 || at == len(field)-1 {
			return nil, fmt.Errorf("-only %q: each target is file.go:line", field)
		}
		line, err := strconv.Atoi(field[at+1:])
		if err != nil || line <= 0 {
			return nil, fmt.Errorf("-only %q: %q is not a line number", field, field[at+1:])
		}
		out = append(out, target{file: field[:at], line: line})
	}
	return out, nil
}

// collect finds every refusal guard in the chosen files.
//
// A refusal is an `if` with no initialiser, no else, and a body that ends in a
// return. The initialiser matters: deleting `if err := f(); err != nil { ... }`
// deletes the CALL too, which is a different and larger change than the one
// being reported.
// partitionByBuild splits files into the ones the go tool would compile for the
// platform this process is pointed at, and the ones it would not.
//
// It reads GOOS and GOARCH the way the go tool does, through [build.Default],
// so `GOOS=js GOARCH=wasm mutsweep` and `GOOS=js GOARCH=wasm go test` agree
// about which files exist.
func partitionByBuild(dir string, paths []string) (in, excluded []string, err error) {
	ctx := build.Default
	for _, p := range paths {
		full := filepath.Join(dir, p)
		ok, mErr := ctx.MatchFile(filepath.Dir(full), filepath.Base(full))
		if mErr != nil {
			return nil, nil, fmt.Errorf("%s: %w", full, mErr)
		}
		if ok {
			in = append(in, p)
		} else {
			excluded = append(excluded, p)
		}
	}
	return in, excluded, nil
}

// excludedError says what an excluded file means, because the number it would
// otherwise produce is worse than no number.
//
// A file the build leaves out is not compiled by the command, so deleting a
// refusal in it changes nothing the command can see and the suite passes. That
// is reported as SURVIVED -- "nothing covers this" -- when the truth is that
// nothing LOOKED. Measured on go-crdt/collab, whose root holds 286 refusals of
// which 41 are under `js && wasm`: a native sweep of those files invents 41
// survivors, each after a full minute of running a suite that never compiled
// them.
//
// The message names a command that RUNS the tests rather than one that only
// builds them, because the first draft of it suggested `go vet` and that was
// advice leading to a verdict worth nothing: vet compiles the file, so the
// mutant is a mutant, but vet cannot fail for any mutation that still compiles
// -- every refusal comes back SURVIVED in a second. A sweep needs a command
// that can go red.
func excludedError(dir string, excluded []string) error {
	return fmt.Errorf("the %s/%s build excludes %s — a refusal there is not compiled by the command, "+
		"so deleting it changes nothing and every one would be reported as SURVIVED when nothing looked. "+
		"Sweep them with the platform set on BOTH this command and the one it runs, and with a command that "+
		"RUNS the tests rather than one that only builds them (a build-only command cannot go red for a "+
		"mutation that still compiles, so every refusal would survive it), e.g. "+
		"`GOOS=js GOARCH=wasm mutsweep -dir %s -files %s -- go test -exec \"$(go env GOROOT)/lib/wasm/go_js_wasm_exec\" .`",
		build.Default.GOOS, build.Default.GOARCH, strings.Join(excluded, ", "), dir, strings.Join(excluded, ","))
}

// readSource is os.ReadFile behind a name: a file that passes the build check
// and then cannot be read is a race, and no test can arrange one.
var readSource = os.ReadFile

func collect(cfg config) ([]guard, []string, error) {
	g, excluded, _, err := collectGen(cfg)
	return g, excluded, err
}

// isGenerated reports whether src carries Go's generated-code marker
// (https://go.dev/s/generatedcode): a line `// Code generated ... DO NOT EDIT.`
// before the first non-comment text. It is go/ast.IsGenerated, not a regexp, so the
// rule is the standard library's -- a marker after the package clause does not count.
// A file that does not parse is not called generated: the walk reports the error.
func isGenerated(src []byte) bool {
	f, err := parser.ParseFile(token.NewFileSet(), "", src, parser.PackageClauseOnly|parser.ParseComments)
	return err == nil && ast.IsGenerated(f)
}

// collectGen is collect that also returns the generated files it left out. A
// directory sweep skips them -- the generator is tested by its own project -- and
// the caller says so; naming one with -files or -only is an error, because a file
// swept silently or skipped silently reads as a clean sweep.
func collectGen(cfg config) ([]guard, []string, []string, error) {
	paths := cfg.files
	if len(paths) == 0 && len(cfg.only) > 0 {
		// The targets name their files, so there is no reason to parse the rest
		// of the package to throw it away.
		seen := map[string]bool{}
		for _, t := range cfg.only {
			if !seen[t.file] {
				seen[t.file] = true
				paths = append(paths, t.file)
			}
		}
		sort.Strings(paths)
	}
	named := len(paths) > 0
	if len(paths) == 0 {
		entries, err := os.ReadDir(cfg.dir)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, e := range entries {
			n := e.Name()
			if !e.IsDir() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
				paths = append(paths, n)
			}
		}
		sort.Strings(paths)
	}

	// Naming a file is a claim about it, so an excluded one named explicitly is
	// an error. A directory sweep is not a claim about every file in it, so
	// those are skipped -- and returned, because skipping them silently is how
	// a campaign comes to believe it covered a package it never compiled.
	paths, excluded, err := partitionByBuild(cfg.dir, paths)
	if err != nil {
		return nil, nil, nil, err
	}
	if named && len(excluded) > 0 {
		return nil, nil, nil, excludedError(cfg.dir, excluded)
	}

	var out []guard
	var generated []string
	for _, p := range paths {
		// Relative to -dir, because the command and mutate both run THERE:
		// a path that is right for this process is wrong for them.
		src, err := readSource(filepath.Join(cfg.dir, p))
		if err != nil {
			return nil, nil, nil, err
		}
		if isGenerated(src) {
			if named {
				return nil, nil, nil, fmt.Errorf("%s is generated code (it carries a `// Code generated ... DO NOT EDIT.` line): "+
					"its generator is tested by its own project and mutsweep does not sweep it", p)
			}
			generated = append(generated, p)
			continue
		}
		found, err := guardsInSites(cfg.dir, p, src, cfg.boundary(), cfg.allSites())
		if err != nil {
			return nil, nil, nil, err
		}
		out = append(out, found...)
	}
	if len(cfg.only) > 0 {
		g, kErr := keepOnly(out, cfg.only)
		if kErr != nil && !cfg.allSites() {
			// ⛔ "Not there" was FALSE for a line the file holds and this mode does
			// not visit (#21): the reader was sent to look for a typo. Say which.
			all := cfg
			all.sites, all.only = sitesAll, nil
			more, _, _ := collect(all) // the walk just succeeded on the same files
			kErr = fmt.Errorf("%w; %s", kErr, elsewhere(more, cfg.only))
		}
		return g, excluded, generated, kErr
	}
	return out, excluded, generated, nil
}

// guardsIn finds the refusals in one file's SOURCE, given as bytes rather than
// read from disk.
//
// ⛔ It takes the bytes because the push gate (hook.go) must judge what a push
// SENDS, which is the committed blob at HEAD, and that is not always the file on
// disk. Reading the working tree there would let an uncommitted edit decide
// whether a commit is covered. There is ONE walk, and the sweep and the gate both
// call it: a second copy written for the gate would be a second definition of
// "a refusal", free to disagree with the one that was actually swept.
func guardsIn(dir, p string, src []byte, boundary bool) ([]guard, error) {
	return guardsInSites(dir, p, src, boundary, false)
}

// elsewhere names the -only targets that are sites of a shape the default walk
// does not visit, so "not there" is never said of a line that is.
func elsewhere(all []guard, want []target) string {
	var held []string
	for _, w := range want {
		for _, g := range all {
			if g.kind != "" && g.file == w.file && g.line == w.line {
				held = append(held, fmt.Sprintf("%s is a %s site", w, g.kind))
				break
			}
		}
	}
	if len(held) == 0 {
		return "none of them is a site of any shape either"
	}
	return strings.Join(held, ", ") + ": mutsweep visits those only with -sites all"
}

// kindOf renders a guard's kind for the report: empty for the original refusals.
func kindOf(kind, cond string) string { return "[" + kind + "] " + cond }

// guardsInSites is guardsIn with the sites of #21 behind all: a refusal that
// ends in continue or break ([loop]), every other if ([branch]) and a boolean
// return expression ([return=true], [return=false]).
func guardsInSites(dir, p string, src []byte, boundary, all bool) ([]guard, error) {
	full := filepath.Join(dir, p)
	var out []guard
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, full, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	ast.Inspect(f, func(n ast.Node) bool {
		// A statement list is not always a BlockStmt: a case in a switch
		// and a comm clause in a select hold bare []ast.Stmt, and a walk
		// that forgets them is blind where it claims to look.
		var list []ast.Stmt
		switch v := n.(type) {
		case *ast.BlockStmt:
			list = v.List
		case *ast.CaseClause:
			list = v.Body
		case *ast.CommClause:
			list = v.Body
		case *ast.ForStmt:
			// A loop's own condition is an edge too: the walk that stops one
			// element early or late is the off-by-one a table bound hides.
			if boundary && v.Cond != nil {
				out = append(out, boundaries(dir, p, fset, string(src), v.Cond)...)
			}
			return true
		default:
			return true
		}
		for _, stmt := range list {
			if ret, isRet := stmt.(*ast.ReturnStmt); isRet && all {
				out = append(out, returnSites(dir, p, fset, string(src), ret, boundary)...)
				continue
			}
			ifs, ok := stmt.(*ast.IfStmt)
			if !ok {
				continue
			}
			// ⛔⛔ AN `else if` IS NOT AN ELEMENT OF ANY STATEMENT LIST. It lives
			// in the Else field of the `if` before it, so the walk above reaches
			// its BODY (a BlockStmt) and never its CONDITION. The chain is
			// followed here for that reason, and the whole chain is one `for`
			// rather than a recursion so an `else if else if` cannot be the shape
			// that gets forgotten next.
			chained := false
			for cur := ifs; cur != nil; {
				out = append(out, guardsOfIf(dir, p, fset, src, cur, boundary, chained, all)...)
				next, isChain := cur.Else.(*ast.IfStmt)
				if !isChain {
					break
				}
				cur, chained = next, true
			}
		}
		return true
	})
	return out, nil
}

// guardsOfIf is the refusal one `if` is, if it is one: a short body whose last
// statement is a return, which is what makes it a REFUSAL rather than a branch.
//
// ⛔⛔ AN `else` BRANCH USED TO DISQUALIFY IT, and that exclusion was invisible:
// not swept, not counted, not reported, so a package of `if`/`else` guards read
// as a clean sweep. Measured at v0.13.0 against a control file of five shapes,
// which is TestCollectSeesAnIfThatHasAnElseBranch:
//
//	if x > 0 { … }                        enumerated
//	if v, ok := m[k]; ok { … }            enumerated (#29)
//	if x > 10 { … } else if x > 5 { … }   NEITHER condition
//	if x > 3 { … } else { … }             nothing
//	the same two branches with no else    enumerated
//
// The last two are the control: same condition, same branches, differing only
// by the `else`, and only one of them was swept.
//
// ⛔ DELETION IS THE WRONG MUTATION HERE, which is probably why it was excluded:
// deleting `if C { A } else { B }` removes BOTH arms, which is not "the guard
// removed" but "the decision removed", and the suite would go red for a reason
// that says nothing about C. The faithful mutation is the one #29 already added
// for Init clauses — the condition becomes `false && (C)`, every arm stays
// compilable, and the else arm runs unconditionally. So this needed no new
// mutation, only the removal of a skip.
func guardsOfIf(dir, p string, fset *token.FileSet, src []byte, ifs *ast.IfStmt, boundary, chained, all bool) []guard {
	// ⛔ The last statement, or nil for an empty body — and the nil is the point.
	// This used to read `len(List) == 0 || len(List) > 3` followed by an index of
	// `len-1`, which made the emptiness check a refusal NO TEST COULD HOLD: delete
	// it and the next line indexes -1, so the suite goes red with a PANIC rather
	// than with an assertion, and a panic red is luck (the next change that reorders
	// these two lines makes the same mutant survive silently). A nil is not an
	// *ast.ReturnStmt, so an empty body is refused by the test below, by the same
	// sentence that refuses a body that does not end in a return.
	var last ast.Stmt
	if n := len(ifs.Body.List); n > 0 {
		last = ifs.Body.List[n-1]
	}
	kind := ""
	refusal := len(ifs.Body.List) <= 3
	if _, isReturn := last.(*ast.ReturnStmt); refusal && !isReturn {
		// A loop that skips an element is refusing it (#21): continue and break
		// are the refusals of an iteration, as return is of a call.
		if br, isBr := last.(*ast.BranchStmt); all && isBr && (br.Tok == token.CONTINUE || br.Tok == token.BREAK) {
			kind = "loop"
		} else {
			refusal = false
		}
	}
	if !refusal {
		if !all {
			return nil
		}
		kind = "branch"
	}
	if boundary {
		gs := boundaries(dir, p, fset, string(src), ifs.Cond)
		if kind != "" {
			for i := range gs {
				gs[i].kind, gs[i].cond = kind, kindOf(kind, gs[i].cond)
			}
		}
		return gs
	}
	start := fset.Position(ifs.Pos())
	end := fset.Position(ifs.End())
	text := string(src[start.Offset:end.Offset])
	// Take the newline with it, so deleting the guard does not leave a blank
	// line where it stood and gofmt has nothing to say about the mutant.
	if end.Offset < len(src) && src[end.Offset] == '\n' {
		text += "\n"
	}
	from, to := unique(string(src), start.Offset, start.Offset+len(text))
	var cond strings.Builder
	printer.Fprint(&cond, fset, ifs.Cond)
	g := guard{
		dir:  dir,
		file: p,
		line: start.Line,
		cond: strings.Join(strings.Fields(cond.String()), " "),
		from: from,
		to:   to,
	}
	// The second mutation: the condition becomes `false && (C)`. The Init
	// statement and every use of its variables stay, so it compiles where
	// deleting the whole `if` leaves them unused; the body is unreachable.
	cs, ce := fset.Position(ifs.Cond.Pos()).Offset, fset.Position(ifs.Cond.End()).Offset
	altFrom, _ := unique(string(src), cs, ce)
	altTo := altFrom[:len(altFrom)-(ce-cs)] + "false && (" + string(src[cs:ce]) + ")"
	// Deleting an if that has an Init clause also deletes a statement that may
	// have effects, and deleting one with an Else deletes the other arm, so in
	// both cases it is not "the guard removed" and only false && (C) is
	// faithful. For a plain `if` deletion stays the primary mutation and this is
	// the fallback for when it does not compile.
	//
	// ⛔ `chained` is the third case and it is NOT implied by the other two: the
	// LAST link of an `else if` chain has neither an Init nor an Else of its own,
	// and deleting its bytes leaves a dangling `} else` that does not compile.
	// Its own test asserted this before the code did — the guard is the else's
	// body, and what disqualifies deletion is being somebody's else, not having
	// one.
	if kind != "" {
		g.kind, g.cond = kind, kindOf(kind, g.cond)
	}
	// A [branch] is not a refusal, so deleting it removes a decision and a
	// statement list, which says nothing about the condition: only false && (C).
	if ifs.Init != nil || ifs.Else != nil || chained || kind == "branch" {
		g.from, g.to = altFrom, altTo
	} else {
		g.altFrom, g.altTo = altFrom, altTo
	}
	return []guard{g}
}

// keepOnly narrows a collection to the named refusals, and refuses a name that
// matched nothing.
//
// The refusal is the point. A -only list is written from an earlier report, and
// the two things that make one stale -- the file edited since, or the line
// mistyped -- both end as a sweep of nothing. mutsweep already refuses an empty
// collection, but it would say the walk is wrong when the walk is right and the
// NAME is wrong, which sends the reader to the wrong place.
func keepOnly(all []guard, want []target) ([]guard, error) {
	found := make(map[target]bool, len(want))
	var out []guard
	for _, g := range all {
		t := target{file: g.file, line: g.line}
		// Every mutation on the line, once: a boundary sweep may hold several
		// comparisons on one line, and a target names the line.
		var hit bool
		for _, w := range want {
			if w == t {
				found[t] = true
				hit = true
			}
		}
		if hit {
			out = append(out, g)
		}
	}
	var missing []string
	for _, w := range want {
		if !found[w] {
			missing = append(missing, w.String())
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("-only named %d refusal(s) that are not there: %s — the file has moved on, or the line is mistyped",
			len(missing), strings.Join(missing, ", "))
	}
	return out, nil
}

// boundaries makes one mutation per comparison in a condition: the operator
// moved to the other side of its edge.
//
// Each is addressed as a deletion is: the text from the operator back to the
// start of as many lines as it takes to occur once in the file, with the same
// text but for the operator as its replacement. The operator itself is the
// span's end, so a `<` is never confused with the `<` of a `<=` elsewhere --
// the widening makes the whole of it unique.
func boundaries(dir, file string, fset *token.FileSet, src string, cond ast.Expr) []guard {
	var out []guard
	ast.Inspect(cond, func(n ast.Node) bool {
		b, ok := n.(*ast.BinaryExpr)
		if !ok {
			return true
		}
		flip, ok := flipped[b.Op]
		if !ok {
			return true
		}
		pos := fset.Position(b.OpPos)
		end := pos.Offset + len(b.Op.String())
		from, to := unique(src, pos.Offset, end)
		var c strings.Builder
		printer.Fprint(&c, fset, b)
		out = append(out, guard{
			dir:  dir,
			file: file,
			line: pos.Line,
			cond: fmt.Sprintf("%s  [%s to %s]", strings.Join(strings.Fields(c.String()), " "), b.Op, flip),
			from: from,
			to:   to + flip,
		})
		return true
	})
	return out
}

// returnSites mutates the boolean expressions a return statement hands back:
// each becomes true and false (#21), or, in boundary mode, has its comparisons
// moved. Without type information a boolean expression is one that is
// syntactically a comparison, a && or ||, or a negation.
func returnSites(dir, file string, fset *token.FileSet, src string, ret *ast.ReturnStmt, boundary bool) []guard {
	var out []guard
	for _, e := range ret.Results {
		if !isBool(e) {
			continue
		}
		if boundary {
			gs := boundaries(dir, file, fset, src, e)
			for i := range gs {
				gs[i].kind, gs[i].cond = "return", kindOf("return", gs[i].cond)
			}
			out = append(out, gs...)
			continue
		}
		s, en := fset.Position(e.Pos()), fset.Position(e.End())
		from, to := unique(src, s.Offset, en.Offset)
		var c strings.Builder
		printer.Fprint(&c, fset, e)
		for _, v := range []string{"true", "false"} {
			kind := "return=" + v
			out = append(out, guard{
				dir: dir, file: file, line: s.Line, kind: kind,
				cond: kindOf(kind, strings.Join(strings.Fields(c.String()), " ")),
				from: from, to: to + v,
			})
		}
	}
	return out
}

func isBool(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.ParenExpr:
		return isBool(v.X)
	case *ast.UnaryExpr:
		return v.Op == token.NOT
	case *ast.BinaryExpr:
		switch v.Op {
		case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ, token.LAND, token.LOR:
			return true
		}
	}
	return false
}

// unique widens a span backwards, a line at a time, until the text it covers
// appears exactly once in the file. It returns what to replace and what to
// replace it with: the same context, minus the guard.
func unique(src string, start, end int) (from, to string) {
	at := start
	for {
		from, to = src[at:end], src[at:start]
		if strings.Count(src, from) == 1 || at == 0 {
			return from, to
		}
		// Back to the start of the previous line.
		prev := strings.LastIndexByte(src[:at-1], '\n')
		if prev < 0 {
			at = 0
			continue
		}
		at = prev + 1
	}
}

// pathFrom names the file as the command's working directory sees it, since
// that is where mutate opens it.
func pathFrom(dir, runIn, file string) string {
	rel, err := filepath.Rel(runIn, filepath.Join(dir, file))
	if err != nil {
		return filepath.Join(dir, file)
	}
	return rel
}

func (g guard) pathFrom(runIn string) string { return pathFrom(g.dir, runIn, g.file) }
