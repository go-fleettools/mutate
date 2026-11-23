package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #21: sites the walk never visited. Every shape below is one a guard can hide
// in; the default walk must stay exactly as it was (a record counts it), and
// -sites all must add the rest, named by kind.
const sitesSrc = `package x

func Filter(xs []int) (n int) {
	for _, v := range xs {
		if v <= 0 {
			continue
		}
		if v > 100 {
			break
		}
		n += v
	}
	return n
}

func Branch(x int) int {
	y := 0
	if x > 3 {
		y = 1
	}
	if v := x * 2; v > 8 {
		y += v
	}
	return y
}

func Pass(rel, lim float64) bool {
	return rel <= lim
}

func Twice(a, b int) (bool, bool) {
	return a < b, a < b
}

func Plain(x int) int {
	if x < 0 {
		return 0
	}
	return x
}
`

func sitesKinds(t *testing.T, all, boundary bool) (kinds []string, gs []guard) {
	t.Helper()
	gs, err := guardsInSites(".", "x.go", []byte(sitesSrc), boundary, all)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range gs {
		kinds = append(kinds, g.kind)
	}
	return kinds, gs
}

func TestDefaultWalkVisitsNoNewSite(t *testing.T) {
	kinds, gs := sitesKinds(t, false, false)
	if len(gs) != 1 || kinds[0] != "" || gs[0].cond != "x < 0" {
		t.Fatalf("the default walk changed: %v %v", kinds, gs)
	}
	// guardsIn is the walk the gate and the records count with.
	old, err := guardsIn(".", "x.go", []byte(sitesSrc), false)
	if err != nil || len(old) != 1 {
		t.Fatalf("guardsIn counts %d, %v; a record made before #21 would go stale", len(old), err)
	}
}

func TestAllSitesAddsLoopBranchAndReturn(t *testing.T) {
	_, gs := sitesKinds(t, true, false)
	var got []string
	for _, g := range gs {
		got = append(got, g.cond)
	}
	want := []string{
		"[loop] v <= 0", "[loop] v > 100", "[branch] x > 3", "[branch] v > 8",
		"[return=true] rel <= lim", "[return=false] rel <= lim",
		"[return=true] a < b", "[return=false] a < b", "[return=true] a < b", "[return=false] a < b",
		"x < 0",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("sites:\n got %q\nwant %q", got, want)
	}
	for _, g := range gs {
		mutated := strings.Replace(sitesSrc, g.from, g.to, 1)
		if mutated == sitesSrc || strings.Count(sitesSrc, g.from) != 1 {
			t.Errorf("%s is not addressable by its own text", g.cond)
		}
		if _, err := guardsInSites(".", "x.go", []byte(mutated), false, true); err != nil {
			t.Errorf("%s: mutant does not parse: %v", g.cond, err)
		}
		if g.kind == "branch" && !strings.Contains(g.to, "false && (") {
			t.Errorf("%s: a branch is mutated by false && (C), got %q", g.cond, g.to)
		}
	}
}

func TestReturnSitesAreBooleanExpressionsOnly(t *testing.T) {
	src := "package x\n\nfunc A(a, b int, ok bool) (bool, bool, bool, int, bool) {\n\treturn (a < b), !ok, ok, a + b, a&b == 0\n}\n"
	gs, err := guardsInSites(".", "x.go", []byte(src), false, true)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, g := range gs {
		got = append(got, g.cond)
	}
	want := "[return=true] (a < b)|[return=false] (a < b)|[return=true] !ok|[return=false] !ok|[return=true] a&b == 0|[return=false] a&b == 0"
	if strings.Join(got, "|") != want {
		t.Fatalf("got %q", got)
	}
}

// The second of two identical comparisons is its own site (#21, item 4).
func TestIdenticalReturnComparisonsAreEachAddressable(t *testing.T) {
	_, gs := sitesKinds(t, true, false)
	n := 0
	for _, g := range gs {
		if g.cond == "[return=false] a < b" {
			n++
			if strings.Count(sitesSrc, g.from) != 1 {
				t.Errorf("site %d is ambiguous", n)
			}
		}
	}
	if n != 2 {
		t.Fatalf("%d sites for two identical returns", n)
	}
}

func TestBoundaryWithAllSitesMovesReturnAndBranchComparisons(t *testing.T) {
	_, gs := sitesKinds(t, true, true)
	var got []string
	for _, g := range gs {
		got = append(got, g.cond)
	}
	for _, w := range []string{
		"[loop] v <= 0  [<= to <]", "[branch] x > 3  [> to >=]", "[return] rel <= lim  [<= to <]", "x < 0  [< to <=]",
	} {
		found := false
		for _, c := range got {
			found = found || c == w
		}
		if !found {
			t.Errorf("boundary sites lack %q: %q", w, got)
		}
	}
	// A return that is no comparison offers no edge.
	if ks, _ := sitesKinds(t, false, true); len(ks) != 1 {
		t.Errorf("default boundary sites changed: %v", ks)
	}
}

func TestSitesFlagIsValidated(t *testing.T) {
	var out, errOut bytes.Buffer
	if c := run([]string{"-sites", "most", "-tally"}, &out, &errOut); c != 2 || !strings.Contains(errOut.String(), `-sites "most"`) {
		t.Fatalf("exit %d: %s", c, errOut.String())
	}
}

func TestTallyListsWhatTheDefaultWalkSkips(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x.go", sitesSrc)
	var out, errOut bytes.Buffer
	if c := run([]string{"-dir", dir, "-tally"}, &out, &errOut); c != 0 {
		t.Fatalf("exit %d: %s", c, errOut.String())
	}
	if !strings.Contains(out.String(), "not swept (add -sites all): 2 [branch], 2 [loop], 3 [return=false], 3 [return=true]") {
		t.Errorf("tally does not name what it skipped:\n%s", out.String())
	}
	out.Reset()
	if c := run([]string{"-dir", dir, "-tally", "-sites", "all"}, &out, &errOut); c != 0 || strings.Contains(out.String(), "not swept") {
		t.Errorf("-sites all printed a skipped line (exit %d):\n%s", c, out.String())
	}
	// A file with nothing beyond refusals prints no skipped line.
	write(t, dir, "y.go", "package x\n\nfunc P(x int) int {\n\tif x < 0 {\n\t\treturn 0\n\t}\n\treturn x\n}\n")
	out.Reset()
	if c := run([]string{"-dir", dir, "-files", "y.go", "-tally"}, &out, &errOut); c != 0 || strings.Contains(out.String(), "not swept") {
		t.Errorf("exit %d:\n%s", c, out.String())
	}
}

// -only called a line "not there" when it was there in a shape that mode does
// not visit, which sent the reader to look for a typo.
func TestOnlyNamesALineThatIsASiteOfAnotherShape(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x.go", sitesSrc)
	_, _, err := collect(config{dir: dir, only: []target{{"x.go", 5}, {"x.go", 99}}})
	if err == nil || !strings.Contains(err.Error(), "x.go:5 is a loop site") || !strings.Contains(err.Error(), "-sites all") {
		t.Fatalf("the false 'not there' was not corrected: %v", err)
	}
	_, _, err = collect(config{dir: dir, only: []target{{"x.go", 99}}})
	if err == nil || !strings.Contains(err.Error(), "none of them is a site of any shape either") {
		t.Fatalf("a mistyped line lost its message: %v", err)
	}
	if g, _, err := collect(config{dir: dir, sites: sitesAll, only: []target{{"x.go", 5}}}); err != nil || len(g) != 1 {
		t.Fatalf("-sites all -only x.go:5: %v %v", g, err)
	}
}

// #30 again, one site wider: a full record counting only the default walk is
// stale for a sweep that visits more, and the -only run the gate advises must
// turn it into a partial one that covers the lines asked for.
func TestOnlyWithAllSitesRepairsAFullRecordOfTheDefaultCount(t *testing.T) {
	t.Setenv(recordsEnv, t.TempDir())
	dir := t.TempDir()
	write(t, dir, "x.go", sitesSrc)
	src, err := os.ReadFile(filepath.Join(dir, "x.go"))
	if err != nil {
		t.Fatal(err)
	}
	// What every sweep before #21 filed: a full record of the one refusal.
	if err := storeRecord(record{SHA256: contentKey(src), File: "x.go", Guards: 1}); err != nil {
		t.Fatal(err)
	}
	bin := buildMutate(t)
	var out, errOut bytes.Buffer
	args := []string{"-dir", dir, "-sites", "all", "-only", "x.go:5,x.go:36", "-mutate-bin", bin, "--", "true"}
	run(args, &out, &errOut)
	rec, ok, err := lookupRecord(contentKey(src))
	if err != nil || !ok {
		t.Fatalf("no record: %v\n%s\n%s", err, out.String(), errOut.String())
	}
	if len(rec.Lines) != 2 || rec.Guards <= 1 {
		t.Fatalf("the record stayed full and stale: %+v\n%s\n%s", rec, out.String(), errOut.String())
	}
	if !rec.covers(5, 99) || rec.covers(8, 99) {
		t.Errorf("the repaired record does not answer by membership: %+v", rec)
	}
}
