package impact

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FullExecutions requires actual run and successful terminal events for every
// discovered root. Package-level success or a cached/empty match is insufficient.
func FullExecutions(directory string, source Snapshot) ([]Execution, error) {
	files, err := filepath.Glob(filepath.Join(directory, "race-*", "*.jsonl"))
	if err != nil {
		return nil, err
	}
	seen, runs := map[FailureRoot]Execution{}, map[FailureRoot]bool{}
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 4096), 16<<20)
		for scanner.Scan() {
			var e struct {
				Action, Package, Test string
				Elapsed               float64
			}
			if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
				f.Close()
				return nil, err
			}
			if e.Test == "" || strings.Contains(e.Test, "/") {
				continue
			}
			key := FailureRoot{e.Package, e.Test}
			if e.Action == "run" {
				if runs[key] {
					f.Close()
					return nil, fmt.Errorf("duplicate full-suite root: %v", key)
				}
				runs[key] = true
			}
			if e.Action == "pass" || e.Action == "skip" {
				seen[key] = Execution{Package: e.Package, Root: e.Test, Mode: "full-" + e.Action, Seconds: e.Elapsed}
			}
			if e.Action == "fail" {
				f.Close()
				return nil, fmt.Errorf("failed full-suite root: %v", key)
			}
		}
		err = scanner.Err()
		closeErr := f.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	var result []Execution
	for id, p := range source.Packages {
		for _, root := range p.Roots {
			key := FailureRoot{id, root}
			record, ok := seen[key]
			if !ok || !runs[key] {
				return nil, fmt.Errorf("missing executed full-suite root: %s/%s", id, root)
			}
			result = append(result, record)
		}
	}
	return result, nil
}
