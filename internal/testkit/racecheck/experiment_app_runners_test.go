package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestExperimentAppRunnerShards(t *testing.T) {
	list, err := os.ReadFile(os.Getenv("BENCH_ROOTS"))
	if err != nil {
		t.Fatal(err)
	}
	patterns := weightedPatterns(roots(string(list)), 4, rootCosts[appPackage])
	out, err := json.Marshal(patterns)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("BENCH_PATTERNS"), out, 0o600); err != nil {
		t.Fatal(err)
	}
}
