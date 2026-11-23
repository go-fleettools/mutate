package main

// # WHAT A SWEEP LEAVES BEHIND
//
// A sweep used to leave nothing but its report. That made it an ANALYSIS tool,
// not a gate: nothing failed when it was not run, so nothing could tell a change
// that had been swept from one that had not. On 2026-11-18/19 that was measured
// rather than argued -- two sessions wrote "break-checked" over hand-made
// mutations while this tool sat installed, and when it was finally run it found
// three guards no test held, all in code ALREADY MERGED:
//
//	go-tex/engine lastbox.go:75   len(*list) == 0   SURVIVED
//	go-tex/engine registers.go:56 i < len(p)        SURVIVED
//	go-tex/engine toks.go:112     name == ""        SURVIVED
//
// So a delete-mode sweep now writes a RECORD per file it swept completely, keyed
// by the SHA-256 of the file's content. The push gate (hook.go) asks for the
// record of exactly the bytes being pushed. Editing the file changes the key, so
// a record cannot vouch for code it never saw -- which is the whole property.
//
// ⚠ What the key does NOT see, said rather than implied: the TESTS. A record
// says "these were the verdicts when this file was swept". Deleting the test
// that held a guard leaves the guarded file's bytes, and so its record, intact.
// Keying on every _test.go in the module would invalidate every record on every
// test edit anywhere, and a gate that demands a full re-sweep for a typo in an
// unrelated test is one that gets waived by habit.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// recordsEnv overrides where records live. Tests set it so a sweep of a
// temporary package never writes into the real store.
const recordsEnv = "MUTSWEEP_RECORDS"

// recordDir is where records are kept: under the user CONFIG directory, not the
// cache one. A record is evidence that a sweep happened, and a cache is something
// the system may clear -- a cleared cache would only make the gate refuse (the
// safe direction), but evidence that vanishes on its own is not evidence.
func recordDir() (string, error) {
	if d := os.Getenv(recordsEnv); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "mutsweep", "records"), nil
}

// entry is one guard a sweep left unheld, as the report lists it.
type entry struct {
	Line    int     `json:"line"`
	Verdict verdict `json:"verdict"`
	Cond    string  `json:"cond"`
}

// record is what one file's sweeps established.
//
// Lines names the refusals some sweep of these bytes asked about. A FULL sweep
// leaves it empty and means every one of Guards; an -only sweep names its lines,
// and later -only sweeps of the same bytes add theirs. The gate judges the
// refusals a push CHANGES (#24), so a record that covers exactly those is enough:
// asking a one-line change to sweep the other 96 guards of its file is a price
// that gets the gate waived, and a waived gate is no gate.
type record struct {
	SHA256 string    `json:"sha256"`
	File   string    `json:"file"` // where it was swept from; informational only
	Guards int       `json:"guards"`
	Lines  []int     `json:"lines,omitempty"` // empty: a full sweep of Guards refusals
	Needed []entry   `json:"needed"`          // SURVIVED, HUNG, caught by PANIC
	At     time.Time `json:"at"`
}

// covers reports whether some sweep of these bytes asked about the refusal on
// line, given that the file holds total refusals now.
func (r record) covers(line, total int) bool {
	if len(r.Lines) == 0 {
		// A full sweep. Fewer refusals than the walk finds now means an older
		// mutsweep made it, and the new ones were never deleted.
		return r.Guards >= total
	}
	return slices.Contains(r.Lines, line)
}

// contentKey names a file's bytes.
func contentKey(src []byte) string {
	sum := sha256.Sum256(src)
	return hex.EncodeToString(sum[:])
}

// needsReading is the set report() lists under "what nothing holds".
func needsReading(v verdict) bool {
	return v == survived || v == hung || v == caughtByPanic
}

// lookupRecord returns the record for a content key, and whether there is one.
// A store that does not exist yet is "no record", not an error: it is the state
// of a machine where nothing has been swept.
func lookupRecord(key string) (record, bool, error) {
	dir, err := recordDir()
	if err != nil {
		return record{}, false, err
	}
	b, err := os.ReadFile(filepath.Join(dir, key+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return record{}, false, nil
	}
	if err != nil {
		return record{}, false, err
	}
	var r record
	if err := json.Unmarshal(b, &r); err != nil {
		return record{}, false, fmt.Errorf("record %s: %w", key, err)
	}
	// ⛔ A record names the bytes it was written for. One whose recorded key is
	// not the key it was filed under has been moved or edited by hand, and
	// trusting it would let a record vouch for content it never saw.
	if r.SHA256 != key {
		return record{}, false, fmt.Errorf("record %s holds a sweep of %s", key, r.SHA256)
	}
	return r, true, nil
}

// recordTemp is the part of *os.File that storeRecord uses, and createRecordTemp
// the one place it is made: a write or a close that fails on a file this very
// process just created cannot be arranged on a real filesystem, and the two
// error paths below were held by nothing.
type recordTemp interface {
	Write(p []byte) (int, error)
	Close() error
	Name() string
}

var createRecordTemp = func(dir, pattern string) (recordTemp, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// storeRecord writes a record atomically: about twenty-five sessions share this
// machine, and a half-written record read by a gate would be read as corrupt.
func storeRecord(r record) error {
	dir, err := recordDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := createRecordTemp(dir, ".record-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, r.SHA256+".json"))
}

// writeRecords files what a delete-mode sweep established, one record per file.
//
// A FULL sweep (no -only) ran every refusal in each file it named, so it writes
// a fresh record for each. An -only run asked about a few lines: it replaces
// those lines' verdicts in the record for the same bytes (the "write the tests,
// re-check the survivors" loop), and over bytes with no record it STARTS one
// that names exactly those lines -- which is what the gate asks of a push that
// changed them. A partial record never passes for a full one: Lines says which.
//
// ⛔ A sweep in which mutate could not be started files nothing at all: its
// verdicts are about the tool, not the code.
func writeRecords(cfg config, results []result, stderr io.Writer) error {
	for _, r := range results {
		if r.verdict == unrun {
			return errors.New("mutate could not be started for some guards, so no verdict here is about the code")
		}
	}
	byFile := map[string][]result{}
	var order []string
	for _, r := range results {
		p := filepath.Join(r.dir, r.file)
		if _, seen := byFile[p]; !seen {
			order = append(order, p)
		}
		byFile[p] = append(byFile[p], r)
	}
	now := time.Now().UTC()
	for _, p := range order {
		// The bytes AFTER the sweep: mutate restores each file, and
		// ensureRestored has already refused a sweep that did not.
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		key := contentKey(src)
		rs := byFile[p]
		if len(cfg.only) == 0 {
			rec := record{SHA256: key, File: p, Guards: len(rs), At: now}
			for _, r := range rs {
				if needsReading(r.verdict) {
					rec.Needed = append(rec.Needed, entry{Line: r.line, Verdict: r.verdict, Cond: r.cond})
				}
			}
			if err := storeRecord(rec); err != nil {
				return err
			}
			continue
		}
		rec, ok, err := lookupRecord(key)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintf(stderr, "mutsweep: %s had no record at this content; filed one for the %d "+
				"line(s) asked about, which is not a sweep of the file\n", p, len(rs))
			rec = record{SHA256: key, File: p}
		}
		rerun := map[int]result{}
		for _, r := range rs {
			rerun[r.line] = r
		}
		if !ok || len(rec.Lines) > 0 {
			// A partial record learns which lines it now covers; a full one
			// already covers them all.
			// Membership, not covers(): a record just started has no Lines
			// yet, and covers() would read that as a full sweep.
			for _, r := range rs {
				if !slices.Contains(rec.Lines, r.line) {
					rec.Lines = append(rec.Lines, r.line)
				}
			}
			slices.Sort(rec.Lines)
		}
		var kept []entry
		for _, e := range rec.Needed {
			if _, again := rerun[e.Line]; !again {
				kept = append(kept, e)
			}
		}
		for _, r := range rs {
			if needsReading(r.verdict) {
				kept = append(kept, entry{Line: r.line, Verdict: r.verdict, Cond: r.cond})
			}
		}
		rec.Needed, rec.At = kept, now
		if err := storeRecord(rec); err != nil {
			return err
		}
	}
	return nil
}
