package impact

import (
	"slices"
	"strings"
	"testing"
)

func dependencyFixture() []Package {
	return []Package{
		{ImportPath: module + "internal/leaf", Dir: "internal/leaf"},
		{ImportPath: module + "internal/app", Dir: "internal/app", Imports: []string{module + "internal/leaf"}},
		{ImportPath: module + "internal/api", Dir: "internal/api", Imports: []string{module + "internal/app"}},
		{ImportPath: module + "internal/e2e", Dir: "internal/e2e", XTestImports: []string{module + "internal/api"}},
		{ImportPath: module + "internal/sandbox", Dir: "internal/sandbox", TestImports: []string{module + "internal/app"}},
		{ImportPath: module + "internal/unrelated", Dir: "internal/unrelated"},
	}
}

func TestImplementationFollowsReverseProductionAndExternalTestImports(t *testing.T) {
	p := Select([]string{"internal/leaf/work.go"}, dependencyFixture(), false)
	for _, name := range []string{"leaf", "app", "api", "e2e", "sandbox"} {
		if !slices.Contains(p.Packages, module+"internal/"+name) {
			t.Fatal("cross-component contract omitted", name, p)
		}
	}
	if slices.Contains(p.Packages, module+"internal/unrelated") || p.Web || p.Release || p.Full {
		t.Fatal("leaf change unnecessarily selected unrelated release or frontend work", p)
	}
	if !slices.Equal(p.Groups, []string{"api", "app", "e2e", "other"}) {
		t.Fatal(p.Groups)
	}
}

func TestTestOnlyChangesDoNotPretendProductionImportsChanged(t *testing.T) {
	p := Select([]string{"internal/app/work_test.go"}, dependencyFixture(), false)
	if !slices.Equal(p.Packages, []string{module + "internal/app"}) || !slices.Equal(p.Groups, []string{"app"}) {
		t.Fatal(p)
	}
}

func TestLocalControlRegistrationsRetainCapabilityCoverageWithoutImportEdges(t *testing.T) {
	for _, name := range []string{"runner", "updater", "upgradectl", "maintenance"} {
		packages := append(dependencyFixture(), Package{ImportPath: module + "internal/" + name, Dir: "internal/" + name})
		p := Select([]string{"internal/" + name + "/control.go"}, packages, false)
		if p.Full || !slices.Contains(p.QualityTargets, "capability-check") {
			t.Fatal("source-inspected operator contract omitted", name, p)
		}
		if slices.Contains(p.Packages, module+"internal/api") {
			t.Fatal("source-scanning capability check unnecessarily selected unrelated API runtime tests", name, p)
		}
		p = Select([]string{"internal/" + name + "/control_test.go"}, packages, false)
		if slices.Contains(p.QualityTargets, "capability-check") {
			t.Fatal("test-only change incorrectly changed a production registration", name, p)
		}
	}
}

func TestAssemblyAndDistributionProductionChangesRetainFullBaseline(t *testing.T) {
	for _, dir := range []string{"cmd/tend", "internal/release"} {
		packages := append(dependencyFixture(), Package{ImportPath: module + dir, Dir: dir})
		if p := Select([]string{dir + "/main.go"}, packages, false); !p.Full || !p.Release || !p.Cross {
			t.Fatal("complete artifact contract omitted", dir, p)
		}
		if p := Select([]string{dir + "/main_test.go"}, packages, false); p.Full || len(p.Packages) != 1 {
			t.Fatal("test-only change unnecessarily selected complete release baseline", dir, p)
		}
	}
}

func TestDocumentationAndFrontendKeepDifferentNecessaryCoverage(t *testing.T) {
	p := Select([]string{"docs/plans/example.md"}, nil, false)
	if p.Full || len(p.Packages) != 0 || p.Web || p.Cross || p.Release || !slices.Equal(p.QualityTargets, []string{"diff-check"}) {
		t.Fatal(p)
	}
	p = Select([]string{"web/src/issues/Details.tsx", "web/package.json"}, nil, false)
	if p.Full || len(p.Packages) != 0 || !p.Web || !p.Layout || p.Cross || p.Release {
		t.Fatal(p)
	}
}

func TestBroadOrUnknownInputsRetainCompleteValidation(t *testing.T) {
	for _, file := range []string{"go.mod", "go.sum", "Makefile", ".github/workflows/ci.yml", "internal/protocol/request.go", "web/src/api/generated/api.ts", "internal/store/migrations/next.sql", "internal/testkit/impact/plan.go", "internal/testkit/testphase/context.go", "internal/release/manifest.go", "cmd/tend/main.go", "unknown.json", "docs/examples/tend-server.hujson", "internal/removed/last.go"} {
		t.Run(file, func(t *testing.T) {
			p := Select([]string{file}, dependencyFixture(), false)
			if !p.Full || !p.Web || !p.Layout || !p.Cross || !p.Release || len(p.Groups) != 5 {
				t.Fatal("unsafe incomplete plan", p)
			}
		})
	}
	for _, files := range [][]string{nil, {"docs/a.md"}} {
		p := Select(files, nil, true)
		if !p.Full || len(p.Groups) != 5 {
			t.Fatal("explicit baseline lost complete checks", p)
		}
	}
}

func TestRequiredAggregateRejectsSkippedMissingFailedAndCancelledWork(t *testing.T) {
	for _, files := range [][]string{{"docs/a.md"}, {"web/src/a.tsx"}, {"internal/leaf/a.go"}, {"go.mod"}} {
		p := Select(files, dependencyFixture(), false)
		results := map[string]string{}
		for job, required := range p.Required() {
			if required {
				results[job] = "success"
			} else {
				results[job] = "skipped"
			}
		}
		if err := p.VerifyResults(results); err != nil {
			t.Fatal("planned exclusion incorrectly rejected", err)
		}
		for job, required := range p.Required() {
			original := results[job]
			if required {
				for _, bad := range []string{"skipped", "failure", "cancelled", ""} {
					results[job] = bad
					if err := p.VerifyResults(results); err == nil || !strings.Contains(err.Error(), job) {
						t.Fatal("necessary job accepted", job, bad, err)
					}
				}
			} else {
				results[job] = "failure"
				if err := p.VerifyResults(results); err == nil {
					t.Fatal("unexpected failed job hidden", job)
				}
			}
			delete(results, job)
			if err := p.VerifyResults(results); err == nil {
				t.Fatal("missing job interpreted as planned skip", job)
			}
			results[job] = original
		}
	}
}
