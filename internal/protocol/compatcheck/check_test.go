package compatcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "internal/protocol")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"runner_compat.json": `{"current":1,"previous":0,"adapters":[{"id":"legacy","protocol":0,"remove_in":2,"file":"internal/protocol/legacy.go","test":"internal/protocol/legacy_test.go"}]}`, "legacy.go": "// tend:compat legacy protocol=0 remove=2\n", "legacy_test.go": "package protocol\n"}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
func TestCompatibilityExpiryAndMarkers(t *testing.T) {
	root := fixture(t)
	if err := Check(root, 1); err != nil {
		t.Fatal(err)
	}
	if err := Check(root, 2); err == nil {
		t.Fatal("bumped version retained registry")
	}
	path := filepath.Join(root, "internal/protocol/runner_compat.json")
	body, _ := os.ReadFile(path)
	body = []byte(strings.ReplaceAll(strings.ReplaceAll(string(body), `"current":1`, `"current":2`), `"previous":0`, `"previous":1`))
	os.WriteFile(path, body, 0600)
	if err := Check(root, 2); err == nil {
		t.Fatal("expired adapter accepted")
	}
}
func TestCompatibilityRequiresCodeAndTests(t *testing.T) {
	for _, name := range []string{"legacy.go", "legacy_test.go"} {
		t.Run(name, func(t *testing.T) {
			root := fixture(t)
			os.Remove(filepath.Join(root, "internal/protocol", name))
			if err := Check(root, 1); err == nil {
				t.Fatal("missing evidence accepted")
			}
		})
	}
	root := fixture(t)
	os.WriteFile(filepath.Join(root, "internal/extra.go"), []byte("// tend:compat unknown protocol=0 remove=2\n"), 0600)
	if err := Check(root, 1); err == nil {
		t.Fatal("undeclared adapter accepted")
	}
}
