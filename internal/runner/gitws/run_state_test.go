package gitws

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunStateLocatorDoesNotPrepareOrEscape(t *testing.T) {
	home := filepath.Join(t.TempDir(), "not-created")
	spaces := New(Config{Home: home})
	located, err := spaces.RunStateDirectory("run_original")
	if err != nil || located != filepath.Join(home, "runs", "run_original") {
		t.Fatal(located, err)
	}
	for _, invalid := range []string{"", ".", "..", filepath.Join("..", "other"), filepath.Join(home, "absolute")} {
		if _, err = spaces.RunStateDirectory(invalid); err == nil {
			t.Fatal("accepted invalid predecessor", invalid)
		}
	}
	if _, err = os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("locator prepared state", err)
	}
}
