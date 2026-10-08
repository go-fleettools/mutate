package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type config struct {
	dir     string // where the files to mutate are
	runIn   string // where the command runs; often the module root above dir
	files   []string
	timeout time.Duration
	mutate  string
}

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
	fs.DurationVar(&cfg.timeout, "timeout", 5*time.Minute, "how long one mutant may run before it is called HUNG")
	fs.StringVar(&cfg.mutate, "mutate", "mutate", "the mutate command to drive")
	if err := fs.Parse(argv); err != nil {
		return cfg, nil, 2
	}
	if *files != "" {
		cfg.files = strings.Split(*files, ",")
	}
	if cfg.runIn == "" {
		cfg.runIn = cfg.dir
	}
	cmd := fs.Args()
	if len(cmd) == 0 {
		fmt.Fprintln(stderr, "usage: mutsweep [-dir D] [-files a.go,b.go] [-timeout 5m] -- command...")
		return cfg, nil, 2
	}
	return cfg, cmd, 0
}

// collect finds every refusal guard in the chosen files.
//
// A refusal is an `if` with no initialiser, no else, and a body that ends in a
// return. The initialiser matters: deleting `if err := f(); err != nil { ... }`
// deletes the CALL too, which is a different and larger change than the one
// being reported.
func collect(cfg config) ([]guard, error) {
	paths := cfg.files
	if len(paths) == 0 {
		entries, err := os.ReadDir(cfg.dir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			n := e.Name()
			if !e.IsDir() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
				paths = append(paths, n)
			}
		}
		sort.Strings(paths)
	}

	var out []guard
	for _, p := range paths {
		// Relative to -dir, because the command and mutate both run THERE:
		// a path that is right for this process is wrong for them.
		full := filepath.Join(cfg.dir, p)
		src, err := os.ReadFile(full)
		if err != nil {
			return nil, err
		}
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
			default:
				return true
			}
			for _, stmt := range list {
				ifs, ok := stmt.(*ast.IfStmt)
				if !ok || ifs.Init != nil || ifs.Else != nil || len(ifs.Body.List) == 0 || len(ifs.Body.List) > 3 {
					continue
				}
				if _, isReturn := ifs.Body.List[len(ifs.Body.List)-1].(*ast.ReturnStmt); !isReturn {
					continue
				}
				start := fset.Position(ifs.Pos())
				end := fset.Position(ifs.End())
				text := string(src[start.Offset:end.Offset])
				// Take the newline with it, so deleting the guard does not
				// leave a blank line where it stood and gofmt has nothing to
				// say about the mutant.
				if end.Offset < len(src) && src[end.Offset] == '\n' {
					text += "\n"
				}
				from, to := unique(string(src), start.Offset, start.Offset+len(text))
				var cond strings.Builder
				printer.Fprint(&cond, fset, ifs.Cond)
				out = append(out, guard{
					dir:  cfg.dir,
					file: p,
					line: start.Line,
					cond: strings.Join(strings.Fields(cond.String()), " "),
					from: from,
					to:   to,
				})
			}
			return true
		})
	}
	return out, nil
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
