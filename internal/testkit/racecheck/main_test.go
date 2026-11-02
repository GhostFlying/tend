package main

import (
	"regexp"
	"slices"
	"testing"
)

func TestCIGroupsPartitionPackagesExactlyOnce(t *testing.T) {
	packages := []string{appPackage, e2ePackage,
		"github.com/GhostFlying/tend/internal/api",
		"github.com/GhostFlying/tend/internal/mainthread",
		"github.com/GhostFlying/tend/internal/store",
		"example/new-package"}
	var combined []string
	for _, group := range []string{"app", "api", "mainthread", "e2e", "other"} {
		selected, err := selectGroup(packages, group)
		if err != nil {
			t.Fatal(err)
		}
		combined = append(combined, selected...)
	}
	slices.Sort(combined)
	want := slices.Clone(packages)
	slices.Sort(want)
	if !slices.Equal(combined, want) {
		t.Fatalf("CI groups omit or repeat packages: %v, want %v", combined, want)
	}
	all, err := selectGroup(packages, "all")
	if err != nil || !slices.Equal(all, packages) {
		t.Fatal(all, err)
	}
	if _, err := selectGroup(packages, "typo"); err == nil {
		t.Fatal("unknown CI group accepted")
	}
	if _, err := selectGroup([]string{appPackage}, "e2e"); err == nil {
		t.Fatal("empty CI group accepted")
	}
}

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

func TestCountPreservesRaceTimeoutAndNativeCachingByDefault(t *testing.T) {
	if got := testArgs("2m", 0); !slices.Equal(got, []string{"test", "-race", "-timeout", "2m"}) {
		t.Fatal(got)
	}
	if got := testArgs("2m", 1); !slices.Equal(got, []string{"test", "-race", "-timeout", "2m", "-count", "1"}) {
		t.Fatal(got)
	}
}
