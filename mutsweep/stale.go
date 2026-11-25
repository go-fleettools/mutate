package main

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// staleTemps lists the `.<name>.atomicwrite-<digits>` files under each root.
//
// ⛔ internal/atomicwrite creates its temp file beside the target, so a sweep
// that was killed between the temp file and the rename leaves it in the tree it
// mutates, where `git add -A` commits it (go-fde/veracrypt v0.8.0 shipped
// `.kdf.go.atomicwrite-4168939430`). A sweep refuses to start while one exists:
// it cannot tell a leftover from the temp file of a sweep still running in the
// same tree, and either way the person has to look.
func staleTemps(roots ...string) ([]string, error) {
	seen := map[string]bool{}
	var found []string
	for _, root := range roots {
		if root == "" {
			continue
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			if isAtomicTemp(d.Name()) && !seen[path] {
				seen[path] = true
				found = append(found, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(found)
	return found, nil
}

// isAtomicTemp: a dot, a name, ".atomicwrite-" and a non-empty suffix.
func isAtomicTemp(name string) bool {
	if !strings.HasPrefix(name, ".") {
		return false
	}
	i := strings.LastIndex(name, ".atomicwrite-")
	return i > 0 && i+len(".atomicwrite-") < len(name)
}

// refuseStale is the gate: nil when the tree holds no stale temp file.
func refuseStale(cfg config) error {
	found, err := staleTemps(cfg.dir, cfg.runIn)
	if err != nil {
		return fmt.Errorf("looking for stale .atomicwrite- temp files: %w", err)
	}
	if len(found) == 0 {
		return nil
	}
	return fmt.Errorf("%d stale atomicwrite temp file(s) in the tree, left by a sweep that did not finish "+
		"(or one still running here); inspect and delete them, then run again:\n  %s",
		len(found), strings.Join(found, "\n  "))
}
