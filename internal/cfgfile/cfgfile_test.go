package cfgfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.hujson")
	if err := os.WriteFile(path, []byte(`{
  // a comment
  "name": "tend",
  "wait": "90s",
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var c struct {
		Name string   `json:"name"`
		Wait Duration `json:"wait"`
	}
	if err := Load(path, &c); err != nil || c.Name != "tend" || time.Duration(c.Wait) != 90*time.Second {
		t.Errorf("Load = %+v, %v", c, err)
	}
	if err := os.WriteFile(path, []byte(`{"nmae": "typo"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Load(path, &c); err == nil || !strings.Contains(err.Error(), "nmae") {
		t.Errorf("unknown field accepted: %v", err)
	}
}

func TestPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	if p, _ := Path("~/x"); p != filepath.Join(home, "x") {
		t.Errorf("Path(~/x) = %s", p)
	}
	if p, _ := Path("/abs"); p != "/abs" {
		t.Errorf("Path(/abs) = %s", p)
	}
	wd, _ := os.Getwd()
	if p, _ := Path("./data"); p != filepath.Join(wd, "data") {
		t.Errorf("Path(./data) = %s, want it resolved against the working directory", p)
	}
	if p, _ := Path(""); p != "" {
		t.Errorf("Path(\"\") = %s", p)
	}
}
