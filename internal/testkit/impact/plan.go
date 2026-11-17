// Package impact selects validation from source changes and Go dependencies.
package impact

import (
	"fmt"
	"path"
	"slices"
	"strings"
)

// Package contains the production and test edges reported by go list.
type Package struct {
	ImportPath                         string
	Dir                                string
	Imports, TestImports, XTestImports []string
}

// Plan is a temporary source-bound execution plan, not a validation receipt.
type Plan struct {
	Version        int               `json:"version"`
	Source         Source            `json:"source"`
	Full           bool              `json:"full"`
	Changed        []string          `json:"changed"`
	Reasons        []string          `json:"reasons"`
	Decisions      map[string]string `json:"decisions"`
	Packages       []string          `json:"packages"`
	Groups         []string          `json:"groups"`
	QualityTargets []string          `json:"quality_targets"`
	Web            bool              `json:"web"`
	Layout         bool              `json:"layout"`
	Cross          bool              `json:"cross"`
	Release        bool              `json:"release"`
}

const module = "github.com/GhostFlying/tend/"

// Groups returns the complete partition used by local and Actions race checks.
// Heavy integration packages run independently instead of serializing other.
func Groups() []string {
	return []string{"app", "api", "mainthread", "e2e", "serverkit", "sandbox", "runner", "other"}
}

// Group is the same partition used by racecheck and the Actions matrix.
func Group(pkg string) string {
	groups := Groups()
	for _, group := range groups[:len(groups)-1] {
		if pkg == module+"internal/"+group {
			return group
		}
	}
	return "other"
}

func fullReason(file string) string {
	switch {
	case file == "go.mod" || file == "go.sum":
		return "Go dependency or toolchain contract"
	case file == "Makefile" || strings.HasPrefix(file, ".github/") || strings.HasPrefix(file, ".devcontainer/"):
		return "build, validation or release infrastructure"
	case strings.HasPrefix(file, "internal/testkit/check/") || strings.HasPrefix(file, "internal/testkit/racecheck/") || strings.HasPrefix(file, "internal/testkit/impact") || strings.HasPrefix(file, "internal/testkit/testbudget/") || strings.HasPrefix(file, "internal/testkit/testphase/"):
		return "shared validation selection or execution infrastructure"
	case strings.HasPrefix(file, "internal/protocol/") || strings.HasPrefix(file, "web/src/api/generated/"):
		return "wire or generated API contract"
	case strings.HasPrefix(file, "internal/store/migrations/"):
		return "persistent schema and historical migration contract"
	case (strings.HasPrefix(file, "internal/release/") || strings.HasPrefix(file, "cmd/")) && strings.HasSuffix(file, ".go") && !strings.HasSuffix(file, "_test.go"):
		return "executable assembly and complete distribution identity contract"
	}
	return ""
}

func documentation(file string) bool {
	return strings.HasPrefix(file, "docs/") && strings.HasSuffix(file, ".md") || file == "README.md" || file == "AGENTS.md" || file == "CLAUDE.md" || file == "LICENSE"
}

// API capability coverage parses these control registrations as source, rather
// than importing their packages. Keep this real edge alongside Go dependencies.
func localControlSource(file string) bool {
	for _, root := range []string{"internal/runner/", "internal/updater/", "internal/upgradectl/", "internal/maintenance/"} {
		if strings.HasPrefix(file, root) {
			return true
		}
	}
	return false
}

// Select computes package-level coverage, never a fixed list of test names.
// Implementation changes follow reverse production AND test imports. Test-only
// changes execute their package, without pretending its production API changed.
func Select(files []string, packages []Package, forceFull bool) Plan {
	p := Plan{Version: 1, Changed: slices.Clone(files), Full: forceFull}
	slices.Sort(p.Changed)
	if forceFull {
		p.Reasons = []string{"explicit full mode or main/release baseline"}
		return complete(p)
	}
	byDir := map[string]string{}
	reverse := map[string][]string{}
	for _, pkg := range packages {
		byDir[pkg.Dir] = pkg.ImportPath
		for _, dep := range append(append(slices.Clone(pkg.Imports), pkg.TestImports...), pkg.XTestImports...) {
			reverse[dep] = append(reverse[dep], pkg.ImportPath)
		}
	}
	selected := map[string]bool{}
	production := []string{}
	capability := false
	for _, file := range p.Changed {
		if reason := fullReason(file); reason != "" {
			p.Full = true
			p.Reasons = append(p.Reasons, file+": "+reason)
			continue
		}
		if documentation(file) {
			continue
		}
		if strings.HasPrefix(file, "web/") {
			p.Web = true
			p.Layout = true
			continue
		}
		if strings.HasSuffix(file, ".go") {
			pkg, ok := byDir[path.Dir(file)]
			if !ok {
				p.Full = true
				p.Reasons = append(p.Reasons, file+": removed or undiscovered Go package")
				continue
			}
			selected[pkg] = true
			if !strings.HasSuffix(file, "_test.go") {
				production = append(production, pkg)
				if localControlSource(file) {
					capability = true
					p.Reasons = append(p.Reasons, file+": API capability tests parse local control source registrations")
				}
			}
			if strings.HasPrefix(file, "internal/api/") || strings.HasPrefix(file, "internal/tools/") || strings.HasPrefix(file, "internal/cli/") {
				capability = true
			}
			if strings.HasPrefix(file, "internal/webui/") {
				p.Web = true
				p.Layout = true
				p.Cross = true
			}
			if strings.HasPrefix(file, "internal/release/") || strings.HasPrefix(file, "cmd/") || strings.HasSuffix(file, "_windows.go") || strings.HasSuffix(file, "_darwin.go") {
				p.Cross = true
			}
			continue
		}
		p.Full = true
		p.Reasons = append(p.Reasons, file+": unclassified input; retain complete validation")
	}
	if len(files) == 0 {
		p.Full = true
		p.Reasons = append(p.Reasons, "no changed candidate: retain complete baseline validation")
	}
	if p.Full {
		return complete(p)
	}
	seen := map[string]bool{}
	for len(production) > 0 {
		pkg := production[0]
		production = production[1:]
		if seen[pkg] {
			continue
		}
		seen[pkg] = true
		selected[pkg] = true
		if strings.HasPrefix(pkg, module+"cmd/") {
			p.Cross = true
		}
		production = append(production, reverse[pkg]...)
	}
	groups := map[string]bool{}
	for pkg := range selected {
		p.Packages = append(p.Packages, pkg)
		groups[Group(pkg)] = true
	}
	slices.Sort(p.Packages)
	for group := range groups {
		p.Groups = append(p.Groups, group)
	}
	slices.Sort(p.Groups)
	if len(p.Packages) > 0 {
		p.QualityTargets = []string{"fmt-check", "vet", "staticcheck"}
		if capability {
			p.QualityTargets = append(p.QualityTargets, "capability-check")
		}
		p.Reasons = append(p.Reasons, "Go: changed packages plus reverse production/test dependencies; all discovered roots retained")
	} else {
		p.QualityTargets = []string{"diff-check"}
	}
	if p.Web {
		p.Reasons = append(p.Reasons, "Web: complete web checks and both browser regression groups")
	}
	if len(p.Packages) == 0 && !p.Web {
		p.Reasons = append(p.Reasons, "documentation only: diff validation; backend, browser and release jobs excluded")
	}
	return explain(p)
}

// complete records package patterns, not a graph-derived package count.
func complete(p Plan) Plan {
	p.Web, p.Layout, p.Cross, p.Release = true, true, true, true
	p.Packages = []string{"./..."}
	p.Groups = Groups()
	p.QualityTargets = []string{"go-check"}
	return explain(p)
}

func explain(p Plan) Plan {
	p.Decisions = map[string]string{
		"quality":          "selected: source diff and applicable static contracts",
		"race":             "excluded: no affected Go package",
		"web":              "excluded: no frontend or embedded asset change",
		"layout":           "excluded: no affected browser presentation",
		"cross":            "excluded: no affected executable or platform contract",
		"release-build":    "excluded: PR does not change a fleet-wide release contract; main/release retain full coverage",
		"release-assemble": "excluded: no complete distribution build is required",
	}
	if len(p.Groups) > 0 {
		p.Decisions["race"] = "selected: all discovered roots in affected packages and reverse production/test dependencies"
	}
	if p.Web {
		p.Decisions["web"] = "selected: complete type, lint, format, unit and web build checks"
	} else if p.Cross || p.Release {
		p.Decisions["web"] = "selected: build embedded assets for executable validation; unrelated frontend tests excluded"
	}
	if p.Layout {
		p.Decisions["layout"] = "selected: both existing browser regression groups"
	}
	if p.Cross {
		p.Decisions["cross"] = "selected: affected executable/platform compile contracts"
	}
	if p.Release {
		p.Decisions["release-build"] = "selected: complete multi-architecture release validation"
		p.Decisions["release-assemble"] = "selected: complete fleet bundle verification"
	}
	return p
}

// Required maps stable aggregate job names to the current plan's requirements.
func (p Plan) Required() map[string]bool {
	return map[string]bool{"quality": true, "race": len(p.Groups) > 0, "web": p.Web || p.Cross || p.Release, "layout": p.Layout, "cross": p.Cross, "release-build": p.Release, "release-assemble": p.Release}
}

// VerifyResults distinguishes planned exclusions from missing or failed work.
func (p Plan) VerifyResults(results map[string]string) error {
	for _, job := range []string{"quality", "race", "web", "layout", "cross", "release-build", "release-assemble"} {
		result, exists := results[job]
		if !exists {
			return fmt.Errorf("missing validation job %s", job)
		}
		if p.Required()[job] && result != "success" {
			return fmt.Errorf("required validation %s is %s", job, result)
		}
		if !p.Required()[job] && result != "skipped" {
			return fmt.Errorf("excluded validation %s unexpectedly is %s", job, result)
		}
	}
	return nil
}
