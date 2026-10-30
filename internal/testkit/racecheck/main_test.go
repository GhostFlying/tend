package main

import (
	"regexp"
	"testing"
)

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
