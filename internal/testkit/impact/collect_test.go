package impact

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func fixtureCommand(t *testing.T, dir, program string, args ...string) string {
	t.Helper()
	b, err := output(t.Context(), dir, program, args...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}
func fixtureWrite(t *testing.T, dir, path, body string) {
	t.Helper()
	name := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
func fixtureCommit(t *testing.T, dir string) string {
	t.Helper()
	fixtureCommand(t, dir, "git", "add", ".")
	tree := fixtureCommand(t, dir, "git", "write-tree")
	// Construct immutable fixture history without changing user git identity.
	text := "tree " + tree + "\nauthor Fixture <fixture@example.invalid> 1 +0000\ncommitter Fixture <fixture@example.invalid> 1 +0000\n\nfixture\n"
	if parent, err := output(t.Context(), dir, "git", "rev-parse", "--verify", "HEAD"); err == nil {
		text = "tree " + tree + "\nparent " + strings.TrimSpace(string(parent)) + "\nauthor Fixture <fixture@example.invalid> 1 +0000\ncommitter Fixture <fixture@example.invalid> 1 +0000\n\nfixture\n"
	}
	path := filepath.Join(t.TempDir(), "commit")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	sha := fixtureCommand(t, dir, "git", "hash-object", "-w", "-t", "commit", path)
	fixtureCommand(t, dir, "git", "update-ref", "HEAD", sha)
	return sha
}
func fixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	fixtureCommand(t, dir, "git", "init", "--quiet")
	fixtureWrite(t, dir, "go.mod", "module example.test/fixture\n\ngo 1.27.1\n")
	fixtureWrite(t, dir, ".gitignore", ".local/\n")
	fixtureWrite(t, dir, "value.go", "package fixture\nfunc A(n int) int {if n>0{return 1};return 0}\nfunc B() int {return 2}\n")
	fixtureWrite(t, dir, "value_test.go", `package fixture
import "testing"
func TestA(t *testing.T) {if A(1)!=1 {t.Fatal("A")}}
func TestB(t *testing.T) {if B()!=2 {t.Fatal("B")}}
func FuzzA(f *testing.F) {f.Add(1);f.Fuzz(func(t *testing.T,n int){A(n)})}
func Example() {B()
// Output:
}
`)
	fixtureCommit(t, dir)
	return dir
}

func TestRealCollectionAndCounterfactualFailures(t *testing.T) {
	dir := fixtureRepo(t)
	index, err := Collect(t.Context(), CollectOptions{Dir: dir, Go: "go", Jobs: 2, Procs: 2, Timeout: time.Minute, Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if index.VerifiedFull || len(index.Executions) != 4 {
		t.Fatalf("collection invented full verification: %+v", index)
	}
	index.VerifiedFull = true
	path := filepath.Join(dir, ".local/index.json")
	if err := WriteJSON(path, index); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadIndex(path); err != nil {
		t.Fatal(err)
	}
	fixtureWrite(t, dir, "value.go", "package fixture\nfunc A(n int) int {if n>0{return 9};return 0}\nfunc B() int {return 2}\n")
	plan, err := PlanChecks(t.Context(), dir, "go", path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	selected := plan.Go["example.test/fixture"]
	if plan.Full || !slices.Contains(selected, "TestA") || !slices.Contains(selected, "FuzzA") || slices.Contains(selected, "TestB") {
		t.Fatal(plan)
	}
	// Establish a real failing counterfactual independently of the selector.
	_, err = output(t.Context(), dir, "go", "test", "-race", "-count=1", "-json", "./...")
	if err == nil {
		t.Fatal("mutant unexpectedly passed the full suite")
	}
	if !slices.Contains(selected, "TestA") {
		t.Fatal("full-suite failure was omitted")
	}
	before := plan.Fingerprint
	fixtureWrite(t, dir, "new.data", "changed")
	if err := SameSource(t.Context(), dir, "go", before); err == nil {
		t.Fatal("source drift accepted")
	}
}

func TestIndexIntegrityAndAncestorFallback(t *testing.T) {
	dir := fixtureRepo(t)
	source, err := Capture(t.Context(), dir, "go")
	if err != nil {
		t.Fatal(err)
	}
	index := indexFor(source)
	path := filepath.Join(dir, ".local/index.json")
	if err := WriteJSON(path, index); err != nil {
		t.Fatal(err)
	}
	index.Executions = index.Executions[:len(index.Executions)-1]
	if err := WriteJSON(path, index); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadIndex(path); err == nil {
		t.Fatal("missing root accepted")
	}
	index = indexFor(source)
	index.Source.Commit = strings.Repeat("f", 40)
	if err := WriteJSON(path, index); err != nil {
		t.Fatal(err)
	}
	p, err := PlanChecks(t.Context(), dir, "go", path, "", "")
	if err != nil || !p.Full {
		t.Fatal(p, err)
	}
}

func TestCollectionCancellationDoesNotAttestSuccess(t *testing.T) {
	dir := fixtureRepo(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	index, err := Collect(ctx, CollectOptions{Dir: dir, Go: "go", Jobs: 1, Timeout: time.Millisecond})
	if err == nil || index != nil {
		t.Fatal(fmt.Sprint(index), err)
	}
}
