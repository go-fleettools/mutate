package main

import (
	"strings"
	"testing"
)

// ⛔ The classifier is a PARSER, and it had none of the fuzzing every other
// parser in the fleet gets. It reads two grammars it does not control — the plain
// output of `go test` and the test2json event stream — joins fragments across
// lines and events, indexes into the lines that follow a match, and from all that
// decides a VERDICT a CI gate acts on. Every defect it has had was found by
// hand-built fixtures, which is to say by imagining the input.
//
// These targets assert the properties that must hold for EVERY input, not the
// verdict of any one of them:
//
//  1. it answers at all — no panic, no index out of range, on arbitrary bytes;
//  2. the verdict is one of exactly two values, and the method one of two;
//  3. ⭐ when the input is not a JSON event stream, asking for the JSON reading
//     must give the SAME answer as asking for the plain one, because readEvents
//     reports !ok and classify falls back. A divergence there means the fallback
//     read something the plain reader did not, which is the shape of the defect
//     that let `-json` on a non-JSON command report a method it had not used.
//
// ⛔ What is deliberately NOT asserted is the verdict itself. A fuzzer has no
// oracle for "is this red a panic", and writing one would mean reimplementing the
// rule beside it, so the two would agree by construction and measure nothing —
// see the fixtures in classify_test.go, which run the real go tool, for that half.
func FuzzClassify(f *testing.F) {
	// Seeds: one per shape the rule distinguishes, so the corpus starts on the
	// boundaries rather than at random. They are the committed corpus.
	for _, s := range []string{
		"",
		"\n",
		"FAIL\tx\t0.1s\nFAIL\n",
		"--- FAIL: TestA (0.00s)\n    a_test.go:7: want 1, got 2\nFAIL\n",
		"--- FAIL: TestA (0.00s)\npanic: boom\n\ngoroutine 7 [running]:\nFAIL\n",
		// The #28 shape: the word on an indented assertion line of its own test.
		"--- FAIL: TestA (0.00s)\n    a_test.go:7: empty: panic: runtime error, want err\n--- FAIL: TestB\npanic: boom\n\ngoroutine 9 [running]:\n",
		// A panic whose VALUE prints like an assertion line (review3/framepanic).
		"panic: wrap:\n    decode_test.go:12: bad header\n\ngoroutine 1 [running]:\n",
		// `goroutine ` with no trace, and a trace with no panic: each half alone.
		"panic: boom\n",
		"goroutine 7 [running]:\n",
		// test2json events, including the fragment cases.
		`{"Action":"fail","Package":"x","Test":"TestA"}`,
		`{"Action":"output","Package":"x","Test":"TestA","Output":"    a_test.go:7: want 1\n"}` + "\n" +
			`{"Action":"fail","Package":"x","Test":"TestA"}` + "\n",
		`{"Action":"output","Package":"x","Output":"pan"}` + "\n" +
			`{"Action":"output","Package":"x","Output":"ic: boom\n"}` + "\n" +
			`{"Action":"output","Package":"x","Output":"goroutine 1 [running]:\n"}` + "\n",
		// A subtest's panic filed under the parent, and a name that merely begins
		// like a parent's.
		`{"Action":"output","Package":"x","Test":"TestA","Output":"panic: boom\ngoroutine 1 [running]:\n"}` + "\n" +
			`{"Action":"fail","Package":"x","Test":"TestA/sub"}` + "\n",
		// Not JSON at all, so the -json reading must fall back.
		"{not json\n--- FAIL: TestA\n    a_test.go:7: want 1\n",
		// A line with no trailing newline, and a very long one.
		"--- FAIL: TestA\n    a_test.go:7: want 1",
		"--- FAIL: TestA\n    a_test.go:7: " + strings.Repeat("x", 70000) + "\npanic: boom\n\ngoroutine 1 [running]:\n",
	} {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, out []byte) {
		plain, mPlain := classify(out, false)
		asJSON, mJSON := classify(out, true)

		for _, c := range []struct {
			name string
			v    verdict
			m    how
		}{{"plain", plain, mPlain}, {"-json", asJSON, mJSON}} {
			// Exactly two verdicts come out of here. SURVIVED, HUNG and "not a
			// mutant" are decided before the output is ever read, so one of them
			// appearing would mean the classifier invented a verdict its caller
			// counts in another bucket.
			if c.v != caught && c.v != caughtByPanic {
				t.Fatalf("%s: verdict %q is neither %q nor %q", c.name, c.v, caught, caughtByPanic)
			}
			if c.m != byText && c.m != byJSON {
				t.Fatalf("%s: method %q is neither %q nor %q", c.name, c.m, byText, byJSON)
			}
		}

		// ⭐ The fallback must be an equality, not an approximation: if the bytes
		// are not an event stream, the two readings read the same text and must
		// agree. When they ARE events, the readings are of different grammars and
		// are allowed to differ -- that is the whole reason -json is preferred.
		if mJSON == byText && asJSON != plain {
			t.Fatalf("not an event stream, yet the -json reading fell back to text and answered %q where the plain reading answered %q", asJSON, plain)
		}
	})
}

// ⛔ The two readers feed `decide`, which looks every line's (package, test) up
// in `passed` without checking what is in there. One invariant carries that
// trust: a verdict is about a TEST, so `passed` must never hold a key with an
// empty test name.
//
// It is not cosmetic. `panicked` treats a line whose test is "" as a panic
// nobody can be blamed for and taints the whole package with it
// (`killed[l.pkg] = true`) — but it reaches that branch only after asking
// `passed[testKey{l.pkg, ""}]`. An empty-named entry reading true would make a
// package-level panic look like the output of something that PASSED, and the
// strongest false reassurance this tool can give is exactly that.
func FuzzReadersKeepAVerdictAboutATest(f *testing.F) {
	for _, s := range []string{
		"=== RUN   TestA\n--- FAIL: TestA (0.00s)\n    a_test.go:7: no\nFAIL\tx\t0.1s\n",
		"ok  \tx\t0.1s\nFAIL\ty\t0.2s\n",
		`{"Action":"output","Package":"x","Test":"TestA","Output":"hi\n"}` + "\n" +
			`{"Action":"pass","Package":"y","Test":"TestA"}` + "\n",
		`{"Action":"skip","Package":"x","Test":"TestA"}` + "\n",
		"",
	} {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, out []byte) {
		check := func(name string, passed map[testKey]bool) {
			for k, v := range passed {
				if k.test == "" {
					t.Fatalf("%s: passed holds an entry with no test name (package %q, value %v): a package-level panic would read as the output of something that passed", name, k.pkg, v)
				}
			}
		}
		_, textPassed := readText(out)
		check("text", textPassed)

		if _, eventPassed, ok := readEvents(out); ok {
			check("events", eventPassed)
		}
	})
}
