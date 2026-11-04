package impact

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func nativeSelectedFailure(t *testing.T, dir string, plan Plan, root string) {
	t.Helper()
	if !slices.Contains(plan.Go["example.test/fixture"], root) {
		t.Fatalf("failing consumer omitted: %+v", plan)
	}
	for _, selected := range []bool{false, true} {
		args := []string{"test", "-race", "-count=1", "-json"}
		if selected {
			args = append(args, "-run", "^("+strings.Join(plan.Go["example.test/fixture"], "|")+")$")
		}
		args = append(args, "./...")
		var log bytes.Buffer
		err := Command(t.Context(), dir, &log, nil, "go", args...)
		data := log.Bytes()
		failure := `"Action":"fail","Package":"example.test/fixture","Test":"` + root + `"`
		if err == nil || !bytes.Contains(data, []byte(failure)) {
			t.Fatalf("native mutant did not fail %s (selected=%t): %v\n%s", root, selected, err, data)
		}
		t.Logf("override counterexample selected=%t native events:\n%s", selected, data)
	}
}

func TestExternalFilesystemConsumerRetainsActualFailure(t *testing.T) {
	for _, testOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "production", true: "test_only"}[testOnly], func(t *testing.T) {
			dir, dependency := fixtureRepo(t), t.TempDir()
			fixtureWrite(t, dependency, "go.mod", "module example.test/reader\n\ngo 1.27.1\n")
			fixtureWrite(t, dependency, "reader.go", "package reader\nimport \"os\"\nfunc Read() string {b,err:=os.ReadFile(\"README.md\");if err!=nil{panic(err)};return string(b)}\n")
			fixtureWrite(t, dir, "go.mod", "module example.test/fixture\n\ngo 1.27.1\nrequire example.test/reader v0.0.0\nreplace example.test/reader => "+filepath.ToSlash(dependency)+"\n")
			fixtureWrite(t, dir, "README.md", "original")
			if testOnly {
				fixtureWrite(t, dir, "read_test.go", "package fixture\nimport(\"testing\";\"example.test/reader\")\nfunc TestRead(t *testing.T){if reader.Read()!=\"original\"{t.Fatal(\"resource changed\")}}\n")
			} else {
				fixtureWrite(t, dir, "read.go", "package fixture\nimport \"example.test/reader\"\nfunc Read()string{return reader.Read()}\n")
				fixtureWrite(t, dir, "read_test.go", "package fixture\nimport \"testing\"\nfunc TestRead(t *testing.T){if Read()!=\"original\"{t.Fatal(\"resource changed\")}}\n")
			}
			fixtureCommit(t, dir)
			if _, err := output(t.Context(), dir, "go", "test", "-race", "-count=1", "./..."); err != nil {
				t.Fatal("ordinary baseline failed", err)
			}
			index, err := Collect(t.Context(), CollectOptions{Dir: dir, Go: "go", Jobs: 2, Timeout: time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			if !index.Source.Packages[index.Source.Module].SourceInspection {
				t.Fatal("external dependency's filesystem boundary was lost")
			}
			index.VerifiedFull = true
			path := filepath.Join(dir, ".local/index.json")
			if err := WriteJSON(path, index); err != nil {
				t.Fatal(err)
			}
			fixtureWrite(t, dir, "README.md", "changed")
			plan, err := PlanChecks(t.Context(), dir, "go", path, "", "")
			if err != nil {
				t.Fatal(err)
			}
			nativeSelectedFailure(t, dir, plan, "TestRead")
		})
	}
}

func TestCompilerOverlayContentDriftRequiresCompleteValidation(t *testing.T) {
	for _, location := range []string{"ignored", "external", "quoted_relative"} {
		t.Run(location, func(t *testing.T) {
			dir := fixtureRepo(t)
			backing := filepath.Join(dir, ".local/alternate.go")
			manifest := filepath.Join(dir, ".local/overlay.json")
			if location == "external" {
				backing = filepath.Join(t.TempDir(), "alternate.go")
			}
			if location == "quoted_relative" {
				manifest = ".local/config with spaces/overlay.json"
				backing = ".local/source with spaces/alternate.go"
			}
			initial := "package fixture\nfunc A(n int)int{if n>0{return 1};return 0}\nfunc B()int{return 2}\n"
			if err := os.MkdirAll(filepath.Dir(buildPath(dir, backing)), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(buildPath(dir, backing), []byte(initial), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := WriteJSON(buildPath(dir, manifest), map[string]any{"Replace": map[string]string{"value.go": backing}}); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GOFLAGS", strings.TrimSpace(os.Getenv("GOFLAGS")+" '-overlay="+manifest+"'"))
			before, err := Capture(t.Context(), dir, "go")
			if err != nil || before.Profile.Overlay == "" {
				t.Fatal(before, err)
			}
			if location == "quoted_relative" {
				cwd, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				relative, err := filepath.Rel(cwd, dir)
				if err != nil {
					t.Fatal(err)
				}
				alias, err := Capture(t.Context(), relative, "go")
				if err != nil || alias.Fingerprint != before.Fingerprint || alias.Profile != before.Profile {
					t.Fatal("relative repository spelling changed effective build identity", alias, err)
				}
			}
			if _, err := output(t.Context(), dir, "go", "test", "-race", "-count=1", "./..."); err != nil {
				t.Fatal("ordinary overlay baseline failed", err)
			}
			if _, err := Collect(t.Context(), CollectOptions{Dir: dir, Go: "go"}); err == nil {
				t.Fatal("overlay published a function coverage baseline")
			}
			index := indexFor(before)
			path := filepath.Join(dir, ".local/index.json")
			if err := WriteJSON(path, index); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(buildPath(dir, backing), []byte(strings.Replace(initial, "return 1", "return 9", 1)), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := SameSnapshot(t.Context(), dir, "go", before); err == nil {
				t.Fatal("changed compiler backing source retained valid identity")
			}
			plan, err := PlanChecks(t.Context(), dir, "go", path, "", "")
			if err != nil || !plan.Full || !strings.Contains(strings.Join(plan.Reasons, "\n"), "compiler overlay") {
				t.Fatal(plan, err)
			}
			nativeSelectedFailure(t, dir, plan, "TestA")
		})
	}
}

func TestCompilerOverlayVirtualFilesAndPhysicalInputsRemainBound(t *testing.T) {
	dir, backing := fixtureRepo(t), t.TempDir()
	fixtureWrite(t, backing, "value.go", "package fixture\nfunc A(n int)int{return 1}\nfunc B()int{return 2}\n")
	fixtureWrite(t, backing, "virtual.go", "package fixture\nimport \"testing\"\nfunc TestVirtual(t *testing.T){if B()!=2{t.Fatal(\"B\")}}\n")
	manifest := filepath.Join(dir, ".local/overlay.json")
	if err := WriteJSON(manifest, map[string]any{"Replace": map[string]string{
		"value.go":        filepath.Join(backing, "value.go"),
		"virtual_test.go": filepath.Join(backing, "virtual.go"),
		"value_test.go":   "",
	}}); err != nil {
		t.Fatal(err)
	}
	// The compiler reads the overlay; runtime file APIs can read these physical
	// bytes, so they must still contribute to the before/after identity.
	fixtureWrite(t, dir, "value.go", "incomplete original buffer")
	t.Setenv("GOFLAGS", strings.TrimSpace(os.Getenv("GOFLAGS")+" '-overlay="+manifest+"'"))
	before, err := Capture(t.Context(), dir, "go")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(before.Packages[before.Module].Roots, []string{"TestVirtual"}) {
		t.Fatal("virtual/deleted tests did not match the active compiler view", before.Packages)
	}
	if _, err := output(t.Context(), dir, "go", "test", "-race", "-count=1", "./..."); err != nil {
		t.Fatal("valid overlay did not execute its virtual root", err)
	}
	fixtureWrite(t, dir, "value.go", "changed physical runtime data")
	if err := SameSnapshot(t.Context(), dir, "go", before); err == nil {
		t.Fatal("physical input hidden by compiler overlay was omitted")
	}
}

func TestCompilerOverlayChangeDuringExecutionInvalidatesResult(t *testing.T) {
	tools, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	runner := filepath.Join(t.TempDir(), "racecheck")
	if _, err := output(t.Context(), tools, "go", "build", "-o", runner, "../racecheck"); err != nil {
		t.Fatal(err)
	}
	dir := fixtureRepo(t)
	fixtureWrite(t, dir, "Makefile", "go-check:\n\t@cp .local/new.go .local/active.go\n")
	fixtureWrite(t, dir, ".local/active.go", "package fixture\nfunc A(n int)int{return 1}\nfunc B()int{return 2}\n")
	fixtureWrite(t, dir, ".local/new.go", "package fixture\nfunc A(n int)int{return 9}\nfunc B()int{return 2}\n")
	manifest := filepath.Join(dir, ".local/overlay.json")
	if err := WriteJSON(manifest, map[string]any{"Replace": map[string]string{"value.go": ".local/active.go"}}); err != nil {
		t.Fatal(err)
	}
	fixtureCommit(t, dir)
	t.Setenv("GOFLAGS", strings.TrimSpace(os.Getenv("GOFLAGS")+" '-overlay="+manifest+"'"))
	source, err := Capture(t.Context(), dir, "go")
	if err != nil {
		t.Fatal(err)
	}
	plan := Select(source, indexFor(source), "")
	plan.Web = WebPlan{}
	path := filepath.Join(dir, ".local/plan.json")
	if err := WriteJSON(path, plan); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	result, runErr := Run(t.Context(), RunOptions{Dir: dir, Go: "go", PlanPath: path, RaceRunner: runner, Jobs: 1, Procs: 1, Packages: 1, Count: 1, Log: &log}, plan)
	if runErr == nil || result.Valid || result.Passed || !strings.Contains(result.Error, "source changed") {
		t.Fatal("changed compiler input retained a green source receipt", result, runErr, log.String())
	}
}
