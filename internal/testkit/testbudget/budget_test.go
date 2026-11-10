package testbudget

import (
	"slices"
	"testing"
)

func TestSharedLimitsRetainCoordinatorFormula(t *testing.T) {
	for _, tc := range []struct{ cpus, jobs, packages, processes, procs int }{
		{1, 1, 1, 1, 1}, {2, 1, 1, 1, 2}, {4, 1, 1, 1, 4},
		{8, 2, 2, 4, 2}, {16, 4, 2, 8, 2}, {32, 7, 3, 15, 2},
		{64, 7, 6, 29, 2},
	} {
		if got := Packages(tc.cpus, tc.jobs); got != tc.packages {
			t.Fatalf("Packages(%d,%d)=%d; want %d", tc.cpus, tc.jobs, got, tc.packages)
		}
		if got := Procs(tc.cpus, tc.processes); got != tc.procs {
			t.Fatalf("Procs(%d,%d)=%d; want %d", tc.cpus, tc.processes, got, tc.procs)
		}
	}
}

func TestPackageLimitRecognizesWholeGoFlags(t *testing.T) {
	for _, tc := range []struct {
		flags    string
		want     int
		explicit bool
	}{
		{"", 3, false}, {"-trimpath", 3, false}, {"-trimpath -p=2", 2, true},
		{"'-p=4' -trimpath", 4, true}, {`"-gcflags=all=-N -p=16"`, 3, false},
		{"-p=4 -p=2", 2, true},
	} {
		got, explicit, err := PackageLimit(tc.flags, 3)
		if err != nil || got != tc.want || explicit != tc.explicit {
			t.Fatalf("PackageLimit(%q)=%d,%t,%v", tc.flags, got, explicit, err)
		}
	}
	for _, flags := range []string{"-p=0", "-p=-1", "-p=invalid", `"-p=2`} {
		if _, _, err := PackageLimit(flags, 3); err == nil {
			t.Fatalf("invalid flags accepted: %q", flags)
		}
	}
}

func TestGoEnvironmentOnlyFillsMissingLimits(t *testing.T) {
	for _, tc := range []struct {
		name, flags, procs, wantFlags, wantProcs string
	}{
		{"standalone", "", "", "-p=2", "3"},
		{"unrelated flags", "-trimpath", "", "-trimpath -p=2", "3"},
		{"parent budget", "-trimpath -p=4", "2", "-trimpath -p=4", "2"},
		{"package override", "'-p=4'", "", "'-p=4'", "3"},
		{"thread override", "", "7", "-p=2", "7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := []string{"GOFLAGS=" + tc.flags, "GOMAXPROCS=" + tc.procs, "AUTH=preserved"}
			original := slices.Clone(source)
			env, err := GoEnvironment(source, 2, 3)
			if err != nil || Value(env, "GOFLAGS") != tc.wantFlags || Value(env, "GOMAXPROCS") != tc.wantProcs ||
				Value(env, "AUTH") != "preserved" || !slices.Equal(source, original) {
				t.Fatal(env, err)
			}
			for _, key := range []string{"GOFLAGS=", "GOMAXPROCS="} {
				count := 0
				for _, entry := range env {
					if len(entry) >= len(key) && entry[:len(key)] == key {
						count++
					}
				}
				if count != 1 {
					t.Fatal("ambiguous child environment", env)
				}
			}
		})
	}
}
