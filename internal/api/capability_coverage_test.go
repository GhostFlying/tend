package api

import (
	"fmt"
	"go/ast"
	"go/parser"
	gotoken "go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/cli"
)

func validateCapabilityCoverage(capabilities []string, bindings []cli.CapabilityBinding) error {
	for _, capability := range capabilities {
		found := false
		for _, binding := range bindings {
			if binding.Capability == capability && binding.Command != "" && len(binding.Example) > 0 {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("public capability %q has no executable CLI binding", capability)
		}
	}
	for _, binding := range bindings {
		if !strings.HasPrefix(binding.Capability, "local ") && !slices.Contains(capabilities, binding.Capability) {
			return fmt.Errorf("CLI %q references unregistered capability %q", binding.Command, binding.Capability)
		}
	}
	return nil
}

func TestPublicCapabilityCoverage(t *testing.T) {
	if err := validateCapabilityCoverage(OwnerCapabilities(), cli.CapabilityBindings()); err != nil {
		t.Fatal(err)
	}
}

func TestPublicCapabilityCoverageRejectsMissingAndNewEntries(t *testing.T) {
	capabilities := OwnerCapabilities()
	bindings := cli.CapabilityBindings()
	missing := slices.DeleteFunc(slices.Clone(bindings), func(b cli.CapabilityBinding) bool { return b.Capability == "conversation.compact" })
	if err := validateCapabilityCoverage(capabilities, missing); err == nil {
		t.Fatal("missing command binding passed coverage")
	}
	if err := validateCapabilityCoverage(append(slices.Clone(capabilities), "issues.uncovered"), bindings); err == nil {
		t.Fatal("new operation passed without CLI support")
	}
	if err := validateCapabilityCoverage(append(slices.Clone(capabilities), "GET /api/v1/uncovered"), bindings); err == nil {
		t.Fatal("new route passed without CLI support")
	}
}

// Local operator interfaces are checked against their actual source registrations.
// Updater authorization/health callbacks are executor-only, not operator commands.
func TestPublicCapabilityLocalControlCoverage(t *testing.T) {
	expected := map[string]bool{}

	// Scan every operator-handler source, not a hand-maintained file subset.
	// Runner execution HTTP lives in runnerapi; the updater callbacks below
	// remain explicitly classified as internal machine-authority operations.
	internal := map[string]string{"/updater/authorize": "updater authority binding", "/updater/health": "updater health receipt"}
	get := map[string]bool{"/agents/status": true, "/agents/registration": true, "/repositories": true, "/archive/status": true, "/upgrades/status": true}
	for _, root := range []string{"../runner", "../../cmd/tend-runner", "../upgradectl"} {
		err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(gotoken.NewFileSet(), name, nil, 0)
			if err != nil {
				return err
			}

			record := func(value string) {
				method, path, explicit := strings.Cut(value, " ")
				if explicit && strings.HasPrefix(path, "/") && slices.Contains([]string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}, method) {
					if internal[path] == "" {
						expected["local "+value] = true
					}
					return
				}
				if !strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || internal[value] != "" {
					return
				}
				method = "POST"
				if get[value] {
					method = "GET"
				}
				expected["local "+method+" "+value] = true
			}
			literal := func(expr ast.Expr) {
				if value, ok := expr.(*ast.BasicLit); ok && value.Kind == gotoken.STRING {
					if decoded, err := strconv.Unquote(value.Value); err == nil {
						record(decoded)
					}
				}
			}
			isURLPath := func(expr ast.Expr) bool {
				path, ok := expr.(*ast.SelectorExpr)
				if !ok || path.Sel.Name != "Path" {
					return false
				}
				url, ok := path.X.(*ast.SelectorExpr)
				return ok && url.Sel.Name == "URL"
			}
			// Explicit mux registrations may be factored into helper functions.
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if ok && (selector.Sel.Name == "HandleFunc" || selector.Sel.Name == "Handle") {
					literal(call.Args[0])
				}
				return true
			})
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil || !strings.HasSuffix(fn.Name.Name, "Handler") {
					continue
				}
				ast.Inspect(fn.Body, func(node ast.Node) bool {
					switch expr := node.(type) {
					case *ast.BinaryExpr:
						if expr.Op == gotoken.EQL {
							if isURLPath(expr.X) {
								literal(expr.Y)
							} else if isURLPath(expr.Y) {
								literal(expr.X)
							}
						}
					case *ast.SwitchStmt:
						if isURLPath(expr.Tag) {
							for _, entry := range expr.Body.List {
								for _, choice := range entry.(*ast.CaseClause).List {
									literal(choice)
								}
							}
						}
					}
					return true
				})
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	bindings := cli.CapabilityBindings()
	for capability := range expected {
		found := false
		for _, b := range bindings {
			if b.Capability == capability {
				found = true
			}
		}
		if !found {
			t.Errorf("local operator interface %s lacks a CLI binding", capability)
		}
	}
	for _, b := range bindings {
		if strings.HasPrefix(b.Capability, "local ") && !expected[b.Capability] {
			t.Errorf("CLI references absent local interface %s", b.Capability)
		}
	}
}

func TestPublicCapabilityRoutesUseRegistrationTable(t *testing.T) {
	file, err := parser.ParseFile(gotoken.NewFileSet(), "api.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (selector.Sel.Name != "HandleFunc" && selector.Sel.Name != "Handle") {
			return true
		}
		if len(call.Args) == 0 {
			return true
		}
		if _, literal := call.Args[0].(*ast.BasicLit); literal {
			t.Error("owner routes must use ownerRoutes so CLI coverage sees actual registration")
		}
		return true
	})
}
