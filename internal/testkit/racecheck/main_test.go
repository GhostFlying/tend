package main

import (
	"regexp"
	"slices"
	"testing"
)

func TestPackagePhasesIsolateTransportWithoutLosingCoverage(t *testing.T) {
	input := []string{"example/api", e2ePackage, appPackage, "example/store"}
	others, isolated, app := packagePhases(input)
	if !app || !slices.Equal(others, []string{"example/api", "example/store"}) || !slices.Equal(isolated, []string{e2ePackage}) {
		t.Fatal(others, isolated, app)
	}
	seen := append(append(slices.Clone(others), isolated...), appPackage)
	slices.Sort(seen)
	want := slices.Clone(input)
	slices.Sort(want)
	if !slices.Equal(seen, want) {
		t.Fatal(seen, want)
	}
	others, isolated, app = packagePhases([]string{e2ePackage})
	if app || len(others) != 0 || !slices.Equal(isolated, []string{e2ePackage}) {
		t.Fatal(others, isolated, app)
	}
}

func TestShardPatternsCoverEveryRootExactlyOnce(t *testing.T) {
	names := roots("TestA\nTestAB\nExample\nExampleOutput\nFuzzPayload\nBenchmarkSlow\nok package\n")
	if len(names) != 5 {
		t.Fatal(names)
	}
	for _, count := range []int{1, 2, 8} {
		groups := patterns(names, count)
		for _, name := range names {
			matches := 0
			for _, pattern := range groups {
				if regexp.MustCompile(pattern).MatchString(name) {
					matches++
				}
			}
			if matches != 1 {
				t.Fatalf("%s selected %d times in %d shards", name, matches, count)
			}
		}
		for _, pattern := range groups {
			if regexp.MustCompile(pattern).MatchString("TestAbsent") {
				t.Fatal("shard selected an unlisted target")
			}
		}
	}
}
