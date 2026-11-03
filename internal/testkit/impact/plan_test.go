package impact

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func sourceFor(t *testing.T, path, body string) Snapshot {
	t.Helper()
	f, err := parseFile(path, "example.test/m/core", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return Snapshot{Commit: strings.Repeat("a", 40), Fingerprint: hash([]byte(body)), Module: "example.test/m", Profile: Profile{Go: "test", Race: true}, Files: map[string]File{path: f}, Packages: map[string]Package{
		"example.test/m/core": {ImportPath: "example.test/m/core", Roots: []string{"TestA", "TestB"}},
		"example.test/m/api":  {ImportPath: "example.test/m/api", Imports: []string{"example.test/m/core"}, Roots: []string{"TestHTTP"}},
		"example.test/m/tool": {ImportPath: "example.test/m/tool", TestImports: []string{"example.test/m/core"}, Roots: []string{"TestCLI"}},
		"example.test/m/cmd":  {ImportPath: "example.test/m/cmd", Imports: []string{"example.test/m/tool"}, Roots: []string{"TestIndependent"}},
	}}
}

func indexFor(s Snapshot) *Index {
	x := &Index{Format: Format, Source: s, VerifiedFull: true}
	for pkg, p := range s.Packages {
		for _, root := range p.Roots {
			x.Executions = append(x.Executions, Execution{Package: pkg, Root: root, Mode: "coverage"})
		}
	}
	return x
}

func TestFunctionChangesUseWholeFunctionAndAllReaders(t *testing.T) {
	before := sourceFor(t, "core/a.go", "package core\nfunc Value(n int) int { if n > 0 { return 1 }; return 0 }\nfunc Other() int {return 2}\n")
	index := indexFor(before)
	for i := range index.Executions {
		if index.Executions[i].Root == "TestA" || index.Executions[i].Root == "TestHTTP" {
			index.Executions[i].Functions = []string{"example.test/m/core:Value"}
		}
	}
	after := sourceFor(t, "core/a.go", "package core\nfunc Value(n int) int { if n > 0 { return 1 }; if n < 0 { return 9 }; return 0 }\nfunc Other() int {return 2}\n")
	p := Select(after, index, "")
	if p.Full || !reflect.DeepEqual(p.Go, map[string][]string{"example.test/m/core": {"TestA"}, "example.test/m/api": {"TestHTTP"}}) {
		t.Fatalf("uncovered new branch lost historical callers: %+v", p)
	}
}

func TestDeclarationsExpandProductionAndTerminalTestDependencies(t *testing.T) {
	before := sourceFor(t, "core/a.go", "package core\nconst Limit=1\nfunc Value() int{return Limit}\n")
	after := sourceFor(t, "core/a.go", "package core\nconst Limit=2\nfunc Value() int{return Limit}\n")
	p := Select(after, indexFor(before), "")
	if p.Full || len(p.Go) != 3 || len(p.Go["example.test/m/cmd"]) != 0 {
		t.Fatalf("test dependency propagated as production: %+v", p)
	}
}

func TestInterfaceTypeGlobalAndSignatureContractsRetainAllReaders(t *testing.T) {
	for _, change := range []struct{ name, before, after string }{
		{"interface", "type Reader interface{ Read() int }", "type Reader interface{ Read(int) int }"},
		{"type", "type Config struct{ Limit int }", "type Config struct{ Limit int64 }"},
		{"global", "var Limit = 1", "var Limit = 2"},
		{"signature", "func Value(n int) int { return 1 }", "func Value(n int64) int { return 1 }"},
	} {
		t.Run(change.name, func(t *testing.T) {
			before := sourceFor(t, "core/a.go", "package core\n"+change.before+"\n")
			after := sourceFor(t, "core/a.go", "package core\n"+change.after+"\n")
			p := Select(after, indexFor(before), "")
			want := map[string][]string{"example.test/m/core": {"TestA", "TestB"}, "example.test/m/api": {"TestHTTP"}, "example.test/m/tool": {"TestCLI"}}
			if p.Full || !reflect.DeepEqual(p.Go, want) {
				t.Fatalf("contract change lost its production or terminal test reader: %+v", p)
			}
		})
	}
}

func TestFallbacksNeverProduceAnEmptySuccess(t *testing.T) {
	s := sourceFor(t, "core/a.go", "package core\nfunc Value() int{return 1}\n")
	for _, name := range []string{"missing", "configuration", "unmapped", "new file", "deleted", "migration", "unknown resource"} {
		t.Run(name, func(t *testing.T) {
			index := indexFor(s)
			current := sourceFor(t, "core/a.go", "package core\nfunc Value() int{return 2}\n")
			switch name {
			case "missing":
				index = nil
			case "configuration":
				current.Profile.Go = "different"
			case "new file":
				current.Files["core/new.go"] = current.Files["core/a.go"]
			case "deleted":
				delete(current.Files, "core/a.go")
			case "migration":
				current.Files["internal/store/migrations/9999_new.sql"] = File{Digest: "new"}
			case "unknown resource":
				current.Files["new.data"] = File{Digest: "new"}
			}
			p := Select(current, index, "missing")
			if len(p.Go) == 0 {
				t.Fatalf("lost all validation: %+v", p)
			}
			if name != "unmapped" && name != "new file" && name != "deleted" && !p.Full {
				t.Fatalf("expected full fallback: %+v", p)
			}
		})
	}
}

func TestTestHelperChangesStayWithinItsTestPackage(t *testing.T) {
	before := sourceFor(t, "core/a_test.go", "package core\nfunc helper() int{return 1}\nfunc TestA(t *T){helper()}\nfunc TestB(t *T){}\n")
	after := sourceFor(t, "core/a_test.go", "package core\nfunc helper() int{return 2}\nfunc TestA(t *T){helper()}\nfunc TestB(t *T){}\n")
	p := Select(after, indexFor(before), "")
	if p.Full || len(p.Go) != 1 || len(p.Go["example.test/m/core"]) != 2 {
		t.Fatal(p)
	}
	after = sourceFor(t, "core/a_test.go", "package core\nfunc helper() int{return 1}\nfunc TestA(t *T){helper();helper()}\nfunc TestB(t *T){}\n")
	p = Select(after, indexFor(before), "")
	if !reflect.DeepEqual(p.Go, map[string][]string{"example.test/m/core": {"TestA"}}) {
		t.Fatal(p)
	}
}

func TestMethodsAndInitializers(t *testing.T) {
	before := sourceFor(t, "core/a.go", "package core\ntype Thing struct{}\nfunc (t *Thing) Value() int{return 1}\nfunc init(){}\n")
	index := indexFor(before)
	index.Executions[0].Functions = []string{"example.test/m/core:*Thing.Value"}
	after := sourceFor(t, "core/a.go", "package core\ntype Thing struct{}\nfunc (t *Thing) Value() int{return 2}\nfunc init(){}\n")
	p := Select(after, index, "")
	if len(p.Go) != 1 {
		t.Fatal(p)
	}
	after = sourceFor(t, "core/a.go", "package core\ntype Thing struct{}\nfunc (t *Thing) Value() int{return 1}\nfunc init(){println(1)}\n")
	p = Select(after, index, "")
	if len(p.Go) != 3 {
		t.Fatal(p)
	}
}

func TestWebDataReadersRemainAfterWebAndGoChanges(t *testing.T) {
	for _, reader := range []string{
		`import { readFileSync } from "node:fs";`,
		`const fs = require('fs/promises');`,
		`const helper = import("node:child_process");`,
		"const fs = process.getBuiltinModule(`fs`);",
	} {
		for _, path := range []string{"web/src/value.ts", "core/a.go"} {
			before := sourceFor(t, "core/a.go", "package core\nfunc Value() int{return 1}\n")
			after := sourceFor(t, "core/a.go", "package core\nfunc Value() int{return 2}\n")
			if strings.HasPrefix(path, "web/") {
				after = sourceFor(t, "core/a.go", "package core\nfunc Value() int{return 1}\n")
				before.Files[path], _ = parseFile(path, "", []byte("export const value = 1;"))
				after.Files[path], _ = parseFile(path, "", []byte("export const value = 2;"))
			}
			f, err := parseFile("web/src/read.test.ts", "", []byte(reader))
			if err != nil {
				t.Fatal(err)
			}
			before.Files["web/src/read.test.ts"], after.Files["web/src/read.test.ts"] = f, f
			p := Select(after, indexFor(before), "")
			if p.Full || !p.Web.Run || !p.Web.Full {
				t.Fatalf("data reader lost for %s / %s: %+v", path, reader, p)
			}
		}
	}
}

func TestExamplesOnlyIncludeExecutableRoots(t *testing.T) {
	f, err := parseFile("a_test.go", "m", []byte("package p\nfunc Example_documentation(){}\nfunc Example(){\n// Output:\n}\nfunc Testlower(t *T){}\nfunc TestReal(t *T){}\nfunc FuzzValue(f *F){}\n"))
	if err != nil {
		t.Fatal(err)
	}
	var roots []string
	for _, fn := range f.Functions {
		if fn.Root {
			roots = append(roots, fn.Name)
		}
	}
	slices.Sort(roots)
	if !reflect.DeepEqual(roots, []string{"Example", "FuzzValue", "TestReal"}) {
		t.Fatal(roots)
	}
}

func TestCoverageMapsSpansWithoutDependingOnLineNumbersForSelection(t *testing.T) {
	s := sourceFor(t, "core/a.go", "package core\nfunc A() int {return 1}\nfunc B() int {return 2}\n")
	roots, err := coveredFunctions(strings.NewReader("mode: atomic\nexample.test/m/core/a.go:2.14,2.24 1 1\nexample.test/m/core/a.go:3.14,3.24 1 0\n"), s)
	if err != nil || !reflect.DeepEqual(roots, []string{"example.test/m/core:A"}) {
		t.Fatal(roots, err)
	}
	if _, err := coveredFunctions(strings.NewReader("mode: set\n"), s); err == nil {
		t.Fatal("non-atomic collection accepted")
	}
	if _, err := coveredFunctions(strings.NewReader("mode: atomic\nunknown/file.go:1.1,1.2 1 1\n"), s); err == nil {
		t.Fatal("unknown coverage accepted")
	}
}
