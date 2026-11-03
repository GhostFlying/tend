package impact

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

func allGo(s Snapshot) map[string][]string {
	out := map[string][]string{}
	for id, p := range s.Packages {
		if len(p.Roots) > 0 {
			out[id] = slices.Clone(p.Roots)
		}
	}
	return out
}

func dependentTests(s Snapshot, pkg string) map[string]bool {
	affected := map[string]bool{pkg: true}
	for changed := true; changed; {
		changed = false
		for id, p := range s.Packages {
			if affected[id] {
				continue
			}
			for _, dep := range p.Imports {
				if affected[dep] {
					affected[id], changed = true, true
					break
				}
			}
		}
	}
	tests := map[string]bool{}
	for id, p := range s.Packages {
		if affected[id] {
			tests[id] = true
			continue
		}
		for _, dep := range p.TestImports {
			if affected[dep] {
				tests[id] = true
				break
			}
		}
	}
	return tests
}

func foundation(path string) bool {
	if path == "go.mod" || path == "go.sum" || path == "Makefile" || path == "AGENTS.md" || path == ".gitignore" {
		return true
	}
	for _, prefix := range []string{".github/", ".devcontainer/", "internal/testkit/", "internal/protocol/", "internal/store/migrations/", "internal/runner/journal/migrations/", "cmd/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// Vitest's import graph cannot attribute files read as data or programs started
// by a test/helper. Until those effects are traced, retain the full Web suite
// after any source change when such a boundary exists. This includes tests
// reading Go sources; a Web-only changed-file filter would miss them entirely.
func webSourceInspection(s Snapshot) bool {
	for path, f := range s.Files {
		if !strings.HasPrefix(path, "web/") {
			continue
		}
		switch filepath.Ext(path) {
		case ".ts", ".tsx", ".js", ".jsx", ".mts", ".cts", ".mjs", ".cjs":
			if f.SourceInspection {
				return true
			}
		}
	}
	return false
}

// Select is deterministic and uses the complete baseline-to-candidate diff.
// Historical execution narrows body-only changes. Declarations and unknowns
// expand through production imports, with test imports used as terminal edges.
func Select(current Snapshot, index *Index, invalid string) Plan {
	p := Plan{Format: Format, Commit: current.Commit, Fingerprint: current.Fingerprint, Go: map[string][]string{}, Reasons: []string{}, Layout: []string{}, Profile: current.Profile}
	full := func(reason string) { p.Full = true; p.Reasons = append(p.Reasons, reason) }
	if index == nil {
		full("baseline unavailable: " + invalid)
	} else {
		p.Baseline = index.Source.Commit
		if index.Format != Format || !index.VerifiedFull || current.Profile != index.Source.Profile || current.Module != index.Source.Module {
			full("baseline configuration or verification mismatch")
		}
	}
	if p.Full {
		p.Go, p.Web, p.Cross = allGo(current), WebPlan{Run: true, Full: true}, true
		return p
	}
	available, chosen := map[string]map[string]bool{}, map[string]map[string]bool{}
	for pkg, entry := range current.Packages {
		available[pkg] = map[string]bool{}
		chosen[pkg] = map[string]bool{}
		for _, root := range entry.Roots {
			available[pkg][root] = true
		}
	}
	selectRoot := func(pkg, root string) {
		if available[pkg][root] && !chosen[pkg][root] {
			chosen[pkg][root] = true
			p.Go[pkg] = append(p.Go[pkg], root)
		}
	}
	selectPackage := func(pkg string) {
		for _, r := range current.Packages[pkg].Roots {
			selectRoot(pkg, r)
		}
	}
	widen := func(pkg, reason string, dependencies bool) {
		p.Reasons = append(p.Reasons, reason)
		if pkg == "" {
			full("cannot identify package: " + reason)
			return
		}
		if !dependencies {
			selectPackage(pkg)
			return
		}
		for id := range dependentTests(current, pkg) {
			selectPackage(id)
		}
	}
	paths := map[string]bool{}
	for path := range index.Source.Files {
		paths[path] = true
	}
	for path := range current.Files {
		paths[path] = true
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	slices.Sort(ordered)
	changedSource := false
	for _, path := range ordered {
		if p.Full {
			break
		}
		old, oldOK := index.Source.Files[path]
		now, nowOK := current.Files[path]
		if oldOK && nowOK && old.Digest == now.Digest && old.Executable == now.Executable {
			continue
		}
		changedSource = true
		if oldOK && nowOK && old.Executable != now.Executable {
			full("source permissions changed: " + path)
			continue
		}
		if foundation(path) {
			full("foundation changed: " + path)
			continue
		}
		if strings.HasPrefix(path, "web/") {
			p.Web.Run = true
			p.Web.Base = index.Source.Commit
			ext := filepath.Ext(path)
			lower := strings.ToLower(path)
			if !oldOK || !nowOK || (ext != ".ts" && ext != ".tsx" && ext != ".js" && ext != ".jsx") || strings.Contains(lower, "config") || strings.Contains(lower, "setup") || strings.Contains(lower, "mock") || strings.Contains(lower, "snapshot") || strings.HasPrefix(path, "web/scripts/") {
				p.Web.Full = true
			}
			p.Reasons = append(p.Reasons, "web dependency changed: "+path)
			continue
		}
		if strings.HasSuffix(path, ".md") && (strings.HasPrefix(path, "docs/") || !strings.Contains(path, "/")) {
			p.Reasons = append(p.Reasons, "documentation only: "+path)
			continue
		}
		if !strings.HasSuffix(path, ".go") {
			full("resource or unclassified source changed: " + path)
			continue
		}
		p.Cross = true
		pkg := now.Package
		if pkg == "" {
			pkg = old.Package
		}
		test := strings.HasSuffix(path, "_test.go")
		if !oldOK || !nowOK || old.Header != now.Header || pkg == "" {
			widen(pkg, "declaration or file inventory changed: "+path, !test)
			continue
		}
		for id, fn := range now.Functions {
			previous, exists := old.Functions[id]
			if exists && fn.Digest == previous.Digest {
				continue
			}
			if test {
				if current.Packages[pkg].SharedState || index.Source.Packages[pkg].SharedState {
					widen(pkg, "test package has shared state: "+pkg, false)
				} else if exists && fn.Root {
					selectRoot(pkg, fn.Name)
					p.Reasons = append(p.Reasons, "test body changed: "+id)
				} else {
					widen(pkg, "test helper changed: "+id, false)
				}
				continue
			}
			if fn.Name == "init" {
				widen(pkg, "initializer changed: "+id, true)
				continue
			}
			found := false
			for _, e := range index.Executions {
				if slices.Contains(e.Functions, id) {
					selectRoot(e.Package, e.Root)
					found = true
				}
			}
			if !found {
				widen(pkg, "no historical execution for: "+id, true)
			} else {
				p.Reasons = append(p.Reasons, "executed function changed: "+id)
			}
			for testPkg := range dependentTests(current, pkg) {
				if current.Packages[testPkg].SharedState || index.Source.Packages[testPkg].SharedState {
					widen(testPkg, "test dependency has shared package state: "+testPkg, false)
				}
			}
		}
	}
	// File readers and uninstrumented children can consume Web files or other
	// repository data as well as Go code. Preserve them for any source change;
	// runtime function coverage cannot establish the missing resource edges.
	if changedSource {
		for id, entry := range current.Packages {
			previous := index.Source.Packages[id]
			if entry.ProcessBoundary || previous.ProcessBoundary || entry.SourceInspection || previous.SourceInspection {
				selectPackage(id)
				p.Reasons = append(p.Reasons, "unattributed process or resource inspection after source change: "+id)
			}
		}
	}
	if changedSource && (webSourceInspection(current) || webSourceInspection(index.Source)) {
		p.Web = WebPlan{Run: true, Full: true, Base: index.Source.Commit}
		p.Reasons = append(p.Reasons, "Web filesystem or process boundary requires full source-dependent validation")
	}
	// New roots always run, even if source discovery changes independently of
	// the stored runtime inventory (for example an added executable Example).
	for pkg, entry := range current.Packages {
		for _, root := range entry.Roots {
			if !slices.Contains(index.Source.Packages[pkg].Roots, root) {
				selectRoot(pkg, root)
				p.Reasons = append(p.Reasons, "new root: "+pkg+"/"+root)
			}
		}
	}
	if p.Full {
		p.Go, p.Web, p.Cross = allGo(current), WebPlan{Run: true, Full: true}, true
	}
	for pkg := range p.Go {
		slices.Sort(p.Go[pkg])
	}
	slices.Sort(p.Reasons)
	p.Reasons = slices.Compact(p.Reasons)
	return p
}

func PlanChecks(ctx context.Context, dir, goTool, indexPath, base, force string) (Plan, error) {
	s, err := Capture(ctx, dir, goTool)
	if err != nil {
		return Plan{}, err
	}
	idx, readErr := ReadIndex(indexPath)
	invalid := "missing index"
	if readErr != nil {
		invalid = readErr.Error()
		idx = nil
	}
	if idx != nil {
		if _, err := output(ctx, dir, "git", "merge-base", "--is-ancestor", idx.Source.Commit, s.Commit); err != nil {
			idx = nil
			invalid = "baseline is not an ancestor"
		}
	}
	if idx != nil && base != "" {
		resolved, err := output(ctx, dir, "git", "rev-parse", "--verify", "--end-of-options", base+"^{commit}")
		if err != nil {
			return Plan{}, fmt.Errorf("resolve comparison base: %w", err)
		}
		if _, err := output(ctx, dir, "git", "merge-base", "--is-ancestor", idx.Source.Commit, strings.TrimSpace(string(resolved))); err != nil {
			idx = nil
			invalid = "baseline does not cover requested base"
		}
	}
	p := Select(s, idx, invalid)
	if force != "" {
		p = Select(s, nil, force)
	}
	_, hasWeb := s.Files["web/package.json"]
	if idx != nil {
		_, oldWeb := idx.Source.Files["web/package.json"]
		hasWeb = hasWeb || oldWeb
	}
	if !hasWeb {
		p.Web = WebPlan{}
		p.HistoricalRootSeconds = historicalCost(p, idx)
		return p, nil
	}
	// Runtime scripts use mocked API fixtures, not the production Go server.
	// A full fallback or frontend runtime/resource change includes every layout.
	layoutFull := p.Full
	var scripts map[string]string
	if b, err := os.ReadFile(filepath.Join(dir, "web/package.json")); err == nil {
		var manifest struct {
			Scripts map[string]string `json:"scripts"`
		}
		if json.Unmarshal(b, &manifest) == nil {
			scripts = manifest.Scripts
		}
	}
	paths := map[string]bool{}
	for path := range s.Files {
		paths[path] = true
	}
	if idx != nil {
		for path := range idx.Source.Files {
			paths[path] = true
		}
	}
	partial := map[string]bool{}
	groups, err := os.ReadFile(filepath.Join(dir, "web/scripts/layout-groups.ts"))
	if err != nil {
		return Plan{}, fmt.Errorf("read mandatory browser inventory: %w", err)
	}
	mandatory := map[string]bool{}
	for _, quoted := range regexp.MustCompile(`"test:[a-z-]+"`).FindAllString(string(groups), -1) {
		name := strings.Trim(quoted, `"`)
		if _, ok := scripts[name]; !ok {
			return Plan{}, fmt.Errorf("missing mandatory browser script: %s", name)
		}
		mandatory[name] = true
	}
	if len(mandatory) == 0 {
		return Plan{}, fmt.Errorf("empty mandatory browser inventory")
	}
	for path := range paths {
		if idx != nil && idx.Source.Files[path].Digest == s.Files[path].Digest {
			continue
		}
		if strings.HasPrefix(path, "web/scripts/") && path != "web/scripts/layout-groups.ts" && path != "web/scripts/layout-fixture.ts" {
			matched := false
			for name, command := range scripts {
				if mandatory[name] && strings.Contains(command, strings.TrimPrefix(path, "web/")) {
					partial[name] = true
					matched = true
				}
			}
			if matched {
				continue
			}
		}
		if strings.HasPrefix(path, "web/") {
			layoutFull = true
		}
	}
	if layoutFull {
		// Only mandatory CI entries, not diagnostic helpers, come from groups.
		for name := range mandatory {
			p.Layout = append(p.Layout, name)
		}
		slices.Sort(p.Layout)
		p.Layout = slices.Compact(p.Layout)
	} else {
		for name := range partial {
			p.Layout = append(p.Layout, name)
		}
		slices.Sort(p.Layout)
	}
	p.HistoricalRootSeconds = historicalCost(p, idx)
	return p, nil
}

func historicalCost(p Plan, index *Index) float64 {
	var seconds float64
	if index != nil {
		for _, e := range index.Executions {
			if slices.Contains(p.Go[e.Package], e.Root) {
				seconds += e.Seconds
			}
		}
	}
	return seconds
}
