package main

import (
	"regexp"
	"slices"
	"strconv"
	"testing"

	"github.com/GhostFlying/tend/internal/testkit/testbudget"
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

func TestWeightedShardsSeparateLongRootsAndRemainStableForNewTargets(t *testing.T) {
	names := []string{"TestLongA", "TestLongB", "TestSmallA", "TestSmallB", "ExampleNew", "FuzzNew"}
	costs := map[string]int{"TestLongA": 20, "TestLongB": 18, "TestSmallA": 2, "TestSmallB": 2, "TestRemoved": 1000}
	groups := weightedPatterns(names, 2, costs)
	if len(groups) != 2 {
		t.Fatal(groups)
	}
	for _, pattern := range groups {
		compiled := regexp.MustCompile(pattern)
		if compiled.MatchString("TestLongA") && compiled.MatchString("TestLongB") {
			t.Fatal("long roots assigned to the same shard", groups)
		}
	}
	for _, name := range names {
		matches := 0
		for _, pattern := range groups {
			if regexp.MustCompile(pattern).MatchString(name) {
				matches++
			}
		}
		if matches != 1 {
			t.Fatal("target omitted or repeated", name, groups)
		}
	}
	slices.Reverse(names)
	if !slices.Equal(groups, weightedPatterns(names, 2, costs)) {
		t.Fatal("discovery order changed cache identities")
	}
}

func TestPackageOrderStartsHeavySuitesWithoutOmittingUnknownPackages(t *testing.T) {
	input := []string{"new/package", "light", "heavy", "another/new"}
	original := slices.Clone(input)
	got := orderPackages(input, map[string]int{"heavy": 300, "light": 20, "removed": 1000})
	if !slices.Equal(got, []string{"heavy", "light", "another/new", "new/package"}) || !slices.Equal(input, original) {
		t.Fatal(got, input)
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

func TestRaceBudgetUsesActualPhaseInsteadOfUnselectedAppShards(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		cpus, app, suite      int
		packages              []string
		wantPeak, wantThreads int
	}{
		{"other on small runner", 4, 4, 4, []string{"example/store", "example/sandbox"}, 1, 4},
		{"app on small runner", 4, 4, 1, []string{appPackage}, 4, 1},
		{"api", 8, 4, 2, []string{"github.com/GhostFlying/tend/internal/api"}, 2, 4},
		{"transport", 8, 4, 2, []string{e2ePackage}, 2, 4},
		{"all concurrent shards", 16, 2, 2, []string{appPackage, e2ePackage,
			"github.com/GhostFlying/tend/internal/api", "github.com/GhostFlying/tend/internal/mainthread", "example/store"}, 6, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, peak, err := raceEnvironment(tc.cpus, tc.app, tc.suite, tc.packages, nil)
			if err != nil || peak != tc.wantPeak || testbudget.Value(env, "GOMAXPROCS") != strconv.Itoa(tc.wantThreads) {
				t.Fatal(env, peak, err)
			}
		})
	}
}

func TestRaceAutomaticPhaseBudgetFitsEffectiveCPUs(t *testing.T) {
	selected := []string{appPackage, e2ePackage, "github.com/GhostFlying/tend/internal/api",
		"github.com/GhostFlying/tend/internal/mainthread", "example/store", "example/sandbox", "example/new"}
	for cpus := 1; cpus <= 128; cpus++ {
		shards := max(1, min(4, cpus/8))
		env, peak, err := raceEnvironment(cpus, shards, shards, selected, nil)
		if err != nil {
			t.Fatal(err)
		}
		threads, err := strconv.Atoi(testbudget.Value(env, "GOMAXPROCS"))
		if err != nil || peak*threads > cpus {
			t.Fatalf("effectiveCPUs=%d peak=%d env=%v err=%v", cpus, peak, env, err)
		}
	}
}
